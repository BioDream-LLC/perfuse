package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/awsmsg"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/s3put"
	"github.com/biodream-llc/perfuse/mllp"
)

// Reading from SQS and S3.
//
// Both follow the rule the Kafka source does: nothing is acknowledged to AWS until the channel has handled it. An SQS message is deleted
// after handling and otherwise reappears when its visibility timeout ends; an S3 object is moved or deleted after handling and otherwise
// moved to the error prefix, so one bad file does not stop the ones behind it. Messages are handled one at a time, in the order AWS
// returns them, because order is what a patient's events need.

// startSQSSource begins long-polling the queue.
func (c *Channel) startSQSSource() error {
	cfg := c.cfg.Source.SQS
	if cfg == nil {
		return fmt.Errorf("channel %q has an sqs source with no configuration", c.cfg.Name)
	}
	q, err := awsmsg.NewSQS(awsmsg.Config{Region: cfg.Region, Credentials: cfg.Credentials(), Endpoint: cfg.Endpoint}, cfg.QueueURL)
	if err != nil {
		return err
	}
	c.log.Info("sqs source starting", "queue", cfg.QueueURL, "wait_seconds", cfg.WaitSeconds, "fifo", q.FIFO())

	p := &kafkaPoller{stop: make(chan struct{})}
	c.awsSource = p
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		c.readSQSForever(p, q, cfg)
	}()
	return nil
}

func (c *Channel) readSQSForever(p *kafkaPoller, q *awsmsg.SQS, cfg *config.SQSSource) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-p.stop; cancel() }()

	for {
		select {
		case <-p.stop:
			return
		default:
		}
		msgs, err := q.Receive(ctx, cfg.MaxMessages, cfg.WaitSeconds, int(cfg.VisibilityTimeout/time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Error("reading from sqs failed and will be retried", "queue", cfg.QueueURL, "err", err, "in", 5*time.Second)
			select {
			case <-p.stop:
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, m := range msgs {
			_, err := c.handle(ctx, []byte(m.Body))
			if err == nil {
				// A message the channel could not parse, or could not deliver, was handled without an error and is still not done.
				if outcome := c.lastOutcome.get(); outcome == Failed || outcome == Unparseable {
					err = fmt.Errorf("the message was %s", outcome)
				}
			}
			if err != nil {
				// Not deleted, so SQS offers it again once its visibility timeout ends - and a redrive policy on the queue moves it
				// to a dead-letter queue after however many attempts the queue allows. That is SQS's mechanism, so it is used.
				c.log.Error("a message from sqs could not be handled; sqs will offer it again", "queue", cfg.QueueURL,
					"message_id", m.ID, "receive_count", m.ReceiveCount, "err", err)
				continue
			}
			if err := q.Delete(ctx, m.ReceiptHandle); err != nil {
				c.log.Error("a message was handled but could not be deleted from sqs, so it will arrive again", "message_id", m.ID,
					"err", err)
			}
		}
	}
}

// startS3Source begins polling the bucket prefix.
func (c *Channel) startS3Source() error {
	cfg := c.cfg.Source.S3
	if cfg == nil {
		return fmt.Errorf("channel %q has an s3 source with no configuration", c.cfg.Name)
	}
	client, err := s3put.New(s3put.Config{
		Bucket: cfg.Bucket, Region: cfg.Region, Endpoint: cfg.Endpoint, PathStyle: cfg.PathStyle,
		AccessKeyID: cfg.Credentials().AccessKeyID, SecretAccessKey: cfg.Credentials().SecretAccessKey,
		SessionToken: cfg.Credentials().SessionToken,
	})
	if err != nil {
		return err
	}
	c.log.Info("s3 source starting", "bucket", cfg.Bucket, "prefix", cfg.Prefix, "after_read", cfg.AfterRead,
		"poll_interval", cfg.PollInterval)

	p := &kafkaPoller{stop: make(chan struct{})}
	c.awsSource = p
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		c.readS3Forever(p, client, cfg)
	}()
	return nil
}

func (c *Channel) readS3Forever(p *kafkaPoller, client *s3put.Client, cfg *config.S3Source) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-p.stop; cancel() }()

	for {
		c.pollS3Once(ctx, client, cfg)
		select {
		case <-p.stop:
			return
		case <-time.After(cfg.PollInterval):
		}
	}
}

// pollS3Once reads every matching object currently under the prefix.
func (c *Channel) pollS3Once(ctx context.Context, client *s3put.Client, cfg *config.S3Source) {
	after := ""
	for {
		objs, err := client.List(ctx, cfg.Prefix, after, 1000)
		if err != nil {
			if ctx.Err() == nil {
				c.log.Error("listing the s3 prefix failed and will be retried", "bucket", cfg.Bucket, "prefix", cfg.Prefix, "err", err)
			}
			return
		}
		if len(objs) == 0 {
			return
		}
		for _, o := range objs {
			if ctx.Err() != nil {
				return
			}
			after = o.Key
			if strings.HasSuffix(o.Key, "/") || (cfg.Suffix != "" && !strings.HasSuffix(o.Key, cfg.Suffix)) {
				continue
			}
			c.handleS3Object(ctx, client, cfg, o)
		}
		if len(objs) < 1000 {
			return
		}
	}
}

func (c *Channel) handleS3Object(ctx context.Context, client *s3put.Client, cfg *config.S3Source, o s3put.Object) {
	rel := strings.TrimPrefix(o.Key, cfg.Prefix)

	fail := func(why error) {
		c.log.Error("an s3 object could not be handled; it was moved to the error prefix", "key", o.Key,
			"to", cfg.ErrorPrefix+rel, "err", why)
		if err := client.Copy(ctx, o.Key, cfg.ErrorPrefix+rel); err == nil {
			_ = client.Delete(ctx, o.Key)
		}
	}

	if o.Size > cfg.MaxObjectSize {
		fail(fmt.Errorf("the object is %d bytes and s3.max_object_size is %d", o.Size, cfg.MaxObjectSize))
		return
	}
	body, err := client.Get(ctx, o.Key, cfg.MaxObjectSize)
	if err != nil {
		c.log.Error("reading an s3 object failed and will be retried at the next poll", "key", o.Key, "err", err)
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
				fail(fmt.Errorf("the object is set as framed but the framing is not valid after %d message(s): %w", len(messages), err))
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
		// Every message has to be accepted before the object is disposed of, as with a file: moving an object whose second message
		// failed would lose that message, and the object is the only copy.
		if outcome := c.lastOutcome.get(); outcome == Failed || outcome == Unparseable {
			fail(fmt.Errorf("message %d of %d in the object was %s", i+1, len(messages), outcome))
			return
		}
	}

	if cfg.AfterRead == config.S3AfterReadMove {
		if err := client.Copy(ctx, o.Key, cfg.MoveTo+rel); err != nil {
			// Left in place rather than deleted: an object that cannot be moved is read again next poll, which is a duplicate, and
			// deleting it would be a loss.
			c.log.Error("an s3 object was handled but could not be moved, so it will be read again", "key", o.Key, "err", err)
			return
		}
	}
	if err := client.Delete(ctx, o.Key); err != nil {
		c.log.Error("an s3 object was handled but could not be removed, so it will be read again", "key", o.Key, "err", err)
	}
}

// stopAWSSource ends polling and waits for the message in hand.
func (c *Channel) stopAWSSource() error {
	if c.awsSource == nil {
		return nil
	}
	c.awsSource.close()
	c.awsSource = nil
	return nil
}
