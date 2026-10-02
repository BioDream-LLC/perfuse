package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/s3put"
	"github.com/biodream-llc/perfuse/mllp"
)

// Writing messages into object storage.
//
// Mirth has no S3 connector, so this is somewhere Perfuse is simply ahead rather than catching up, and it
// matters because "archive it" now usually means a bucket rather than a file server.

// S3Sender writes each message as an object.
type S3Sender struct {
	cfg    *config.S3Destination
	name   string
	client *s3put.Client
}

// NewS3Sender builds the sender.
func NewS3Sender(d config.Destination) (*S3Sender, error) {
	if d.S3 == nil {
		return nil, fmt.Errorf("destination %q is an s3 destination with no s3 block", d.Name)
	}

	client, err := s3put.New(s3put.Config{
		Bucket:               d.S3.Bucket,
		Region:               d.S3.Region,
		AccessKeyID:          d.S3.ResolvedAccessKeyID(),
		SecretAccessKey:      d.S3.ResolvedSecretAccessKey(),
		SessionToken:         d.S3.ResolvedSessionToken(),
		Endpoint:             d.S3.Endpoint,
		PathStyle:            d.S3.PathStyle,
		ServerSideEncryption: d.S3.ServerSideEncryption,
		HTTPClient:           nil,
	})
	if err != nil {
		return nil, err
	}

	return &S3Sender{cfg: d.S3, name: d.Name, client: client}, nil
}

// Send writes one object.
func (s *S3Sender) Send(ctx context.Context, msg []byte) error {
	key, err := s.objectKey(msg)
	if err != nil {
		return err
	}

	body := msg
	if s.cfg.Framed {
		body = mllp.Frame(msg)
	}

	contentType := s.cfg.ContentType
	if contentType == "" {
		contentType = "application/hl7-v2"
	}

	// The engine already retries, so this does not. Two layers of retry multiply rather than add, and the
	// result is a destination that keeps trying for an hour when its policy said thirty seconds.
	return s.client.Put(ctx, key, body, contentType)
}

// Describe names the destination for logs and specifications.
//
// The bucket and key pattern, never the credentials. A description ends up in a specification document
// that gets emailed to a vendor.
func (s *S3Sender) Describe() string {
	where := "s3://" + s.cfg.Bucket
	if s.cfg.Endpoint != "" {
		where += " at " + s.cfg.Endpoint
	}
	return where + "/" + s.keyTemplate()
}

// Close releases nothing: the HTTP client is shared and has no state to release.
func (s *S3Sender) Close() error { return nil }

func (s *S3Sender) keyTemplate() string {
	if s.cfg.Key != "" {
		return s.cfg.Key
	}
	return "${date}/${channel}/${control_id}.hl7"
}

// objectKey renders the key for one message.
//
// Deliberately the same placeholder vocabulary as the file and SFTP destinations, and the same treatment
// of a missing control id: something unique rather than an empty string, because two messages sharing a
// key means the second silently replaces the first, and in a bucket there is no ".part" rename to make
// that obvious.
func (s *S3Sender) objectKey(raw []byte) (string, error) {
	msg, err := hl7.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("naming the object needs the message parsed, and a transformation "+
			"produced something invalid: %w", err)
	}

	controlID := ""
	msgType := ""
	if msh, ok := msg.Segment("MSH", 1); ok {
		controlID = msh.Field(10).String()
		msgType = strings.ReplaceAll(msh.Field(9).String(), "^", "_")
	}
	if controlID == "" {
		controlID = fmt.Sprintf("noid-%d", time.Now().UnixNano())
	}

	now := time.Now().UTC()
	out := s.keyTemplate()
	out = strings.ReplaceAll(out, "${timestamp}", now.Format("20060102150405"))
	// Slashes in the date, so the default key partitions by day. A flat prefix makes a bucket slow to
	// list and impossible to lifecycle by age, and nobody notices until there are four million objects.
	out = strings.ReplaceAll(out, "${date}", now.Format("2006/01/02"))
	out = strings.ReplaceAll(out, "${control_id}", sanitiseKeySegment(controlID))
	out = strings.ReplaceAll(out, "${message_type}", sanitiseKeySegment(msgType))
	out = strings.ReplaceAll(out, "${channel}", sanitiseKeySegment(s.name))

	return strings.TrimPrefix(out, "/"), nil
}

// sanitiseKeySegment removes what would change the shape of a key.
//
// Unlike a file name this does not need to defend against directory traversal - S3 has no directories and
// no parent - but a control id containing a slash would silently create a prefix level, so the key would
// no longer match the pattern the operator wrote and nothing downstream would find it.
func sanitiseKeySegment(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return "unnamed"
	}
	return s
}
