package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/azblob"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/mllp"
)

// Azure Blob Storage, both ways: a blob per message out, and blobs picked up from a prefix in. The source follows the S3 source's
// rules - handled blobs move to processed/, ones that cannot be handled to error/, every message in a framed blob accepted first.

func azblobClient(a *config.AzureBlobAccess, tier string) (*azblob.Client, error) {
	return azblob.New(azblob.Config{Account: a.Account, Container: a.Container, Key: a.ResolvedKey(), SAS: a.ResolvedSAS(),
		Endpoint: a.Endpoint, Tier: tier})
}

// AzureBlobSender writes each message as a blob.
type AzureBlobSender struct {
	cfg    *config.AzureBlobDestination
	name   string
	client *azblob.Client
}

// NewAzureBlobSender builds the sender.
func NewAzureBlobSender(d config.Destination) (*AzureBlobSender, error) {
	if d.AzureBlob == nil {
		return nil, fmt.Errorf("destination %q is an azure_blob destination with no azure_blob block", d.Name)
	}
	c, err := azblobClient(&d.AzureBlob.AzureBlobAccess, d.AzureBlob.Tier)
	if err != nil {
		return nil, err
	}
	return &AzureBlobSender{cfg: d.AzureBlob, name: d.Name, client: c}, nil
}

// Send writes one blob.
func (s *AzureBlobSender) Send(ctx context.Context, msg []byte) error {
	name, err := renderObjectKey(s.cfg.Blob, s.name, msg)
	if err != nil {
		return err
	}
	ct := s.cfg.ContentType
	if ct == "" {
		ct = "application/hl7-v2"
	}
	return s.client.Put(ctx, name, msg, ct)
}

// Describe names the container, never the credential.
func (s *AzureBlobSender) Describe() string {
	d := "azure blob " + s.cfg.Account + "/" + s.cfg.Container + "/" + s.cfg.Blob
	if s.cfg.Tier != "" {
		d += " (" + s.cfg.Tier + ")"
	}
	return d
}

// Close releases nothing.
func (s *AzureBlobSender) Close() error { return nil }

func (c *Channel) startAzureBlobSource() error {
	cfg := c.cfg.Source.AzureBlob
	if cfg == nil {
		return fmt.Errorf("channel %q has an azure_blob source with no configuration", c.cfg.Name)
	}
	client, err := azblobClient(&cfg.AzureBlobAccess, "")
	if err != nil {
		return err
	}
	c.log.Info("azure blob source starting", "account", cfg.Account, "container", cfg.Container, "prefix", cfg.Prefix)
	p := &kafkaPoller{stop: make(chan struct{})}
	c.awsSource = p
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { <-p.stop; cancel() }()
		for {
			c.pollAzureBlobOnce(ctx, client, cfg)
			select {
			case <-p.stop:
				return
			case <-time.After(cfg.PollInterval):
			}
		}
	}()
	return nil
}

func (c *Channel) pollAzureBlobOnce(ctx context.Context, client *azblob.Client, cfg *config.AzureBlobSource) {
	marker := ""
	for {
		blobs, next, err := client.List(ctx, cfg.Prefix, marker, 1000)
		if err != nil {
			if ctx.Err() == nil {
				c.log.Error("listing the blob prefix failed and will be retried", "container", cfg.Container, "err", err)
			}
			return
		}
		for _, b := range blobs {
			if ctx.Err() != nil {
				return
			}
			if cfg.Suffix != "" && !strings.HasSuffix(b.Name, cfg.Suffix) {
				continue
			}
			c.handleBlob(ctx, client, cfg, b)
		}
		if next == "" {
			return
		}
		marker = next
	}
}

func (c *Channel) handleBlob(ctx context.Context, client *azblob.Client, cfg *config.AzureBlobSource, b azblob.Blob) {
	rel := strings.TrimPrefix(b.Name, cfg.Prefix)
	var body []byte
	// A move is a put of the same bytes under the new name, then a delete: the bytes are already in hand, so a server-side copy - which
	// is asynchronous in Azure - would add a wait and nothing else.
	move := func(to string) bool {
		if err := client.Put(ctx, to, body, ""); err != nil {
			c.log.Error("a blob could not be moved, so it will be read again", "blob", b.Name, "to", to, "err", err)
			return false
		}
		return true
	}
	fail := func(why error) {
		c.log.Error("a blob could not be handled; it was moved to the error prefix", "blob", b.Name, "err", why)
		if body != nil && move(cfg.ErrorPrefix+rel) {
			_ = client.Delete(ctx, b.Name)
		}
	}
	if b.Size > cfg.MaxObjectSize {
		c.log.Error("a blob is larger than azure_blob.max_object_size and was left where it is", "blob", b.Name, "size", b.Size)
		return
	}
	var err error
	body, err = client.Get(ctx, b.Name, cfg.MaxObjectSize)
	if err != nil {
		c.log.Error("reading a blob failed and will be retried at the next poll", "blob", b.Name, "err", err)
		return
	}
	messages := [][]byte{body}
	if cfg.Framed {
		messages = nil
		r := mllp.NewReader(bytes.NewReader(body), int(cfg.MaxObjectSize))
		for {
			m, err := r.ReadMessage()
			if err == io.EOF {
				break
			}
			if err != nil {
				fail(fmt.Errorf("the blob is set as framed but the framing is not valid: %w", err))
				return
			}
			messages = append(messages, m)
		}
	}
	for i, m := range messages {
		if _, err := c.handle(ctx, m); err != nil {
			fail(err)
			return
		}
		if outcome := c.lastOutcome.get(); outcome == Failed || outcome == Unparseable {
			fail(fmt.Errorf("message %d of %d in the blob was %s", i+1, len(messages), outcome))
			return
		}
	}
	if cfg.AfterRead == config.S3AfterReadMove && !move(cfg.MoveTo+rel) {
		return
	}
	if err := client.Delete(ctx, b.Name); err != nil {
		c.log.Error("a blob was handled but could not be removed, so it will be read again", "blob", b.Name, "err", err)
	}
}
