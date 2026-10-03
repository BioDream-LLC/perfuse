package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/awsv4"
)

// Amazon SQS, SNS and reading from S3.
//
// The S3 destination came first; these complete the set a hospital on AWS asks for: a queue to read from and write to, a topic to
// fan out on, and a bucket to pick files up from. All of them speak signed HTTP through the same signer, so a LocalStack, ElasticMQ
// or VPC endpoint works through endpoint, and none of them pulls in the AWS SDK.

// AWSAccess is how a connector reaches AWS: the region it signs for, the keys, and an optional endpoint override.
//
// Credentials may be literal or ${ENV_VAR} references, which is how they should be supplied. There is no instance-metadata credential
// chain, deliberately: see internal/awsv4.
type AWSAccess struct {
	// Region is required: it forms part of the signature, so a wrong one fails as if the credentials were wrong.
	Region string `yaml:"region"`

	// AccessKeyID is the key id, literal or ${AWS_ACCESS_KEY_ID}.
	AccessKeyID string `yaml:"access_key_id,omitempty"`
	// SecretAccessKey is the secret, literal or ${AWS_SECRET_ACCESS_KEY}. Prefer the reference: this file goes into version control.
	SecretAccessKey string `yaml:"secret_access_key,omitempty"`
	// SessionToken is for temporary credentials only. They expire, and the connector stops working when they do.
	SessionToken string `yaml:"session_token,omitempty"`

	// Endpoint overrides AWS's host: LocalStack, ElasticMQ, a VPC interface endpoint.
	Endpoint string `yaml:"endpoint,omitempty"`
}

// Credentials resolves the keys, expanding environment references.
func (a *AWSAccess) Credentials() awsv4.Credentials {
	return awsv4.Credentials{
		AccessKeyID:     resolveSecret(a.AccessKeyID),
		SecretAccessKey: resolveSecret(a.SecretAccessKey),
		SessionToken:    resolveSecret(a.SessionToken),
	}
}

func (a *AWSAccess) validate(what string) []error {
	var errs []error
	if strings.TrimSpace(a.Region) == "" {
		errs = append(errs, fmt.Errorf("%s needs region; it forms part of the request signature, so a wrong or missing one "+
			"looks like a credentials failure", what))
	}
	c := a.Credentials()
	switch {
	case c.AccessKeyID == "" && c.SecretAccessKey == "":
		errs = append(errs, fmt.Errorf("%s has no AWS credentials; set access_key_id and secret_access_key, or reference the "+
			"environment with ${AWS_ACCESS_KEY_ID} and ${AWS_SECRET_ACCESS_KEY}", what))
	case c.AccessKeyID == "":
		errs = append(errs, fmt.Errorf("%s has an AWS secret but no access key id", what))
	case c.SecretAccessKey == "":
		errs = append(errs, fmt.Errorf("%s has an AWS access key id but no secret", what))
	}
	if a.Endpoint != "" {
		if u, err := url.Parse(a.Endpoint); err != nil || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: endpoint %q is not a URL with a host", what, a.Endpoint))
		}
	}
	return errs
}

// SQSSource reads from an SQS queue.
//
// A message is deleted only after the channel has handled it - delivered, or filtered, or failed in a way the channel records. Until
// then it stays on the queue, invisible for visibility_timeout, and comes back if Perfuse stops. So a message is never lost; the price
// is that one can arrive twice, which SQS itself already allows.
type SQSSource struct {
	AWSAccess `yaml:",inline"`

	// QueueURL names the queue, e.g. https://sqs.eu-west-2.amazonaws.com/123456789012/adt.
	QueueURL string `yaml:"queue_url"`

	// WaitSeconds is the long poll, 1 to 20. Defaults to 20: an empty queue costs one request every twenty seconds, not a loop.
	WaitSeconds int `yaml:"wait_seconds,omitempty"`

	// MaxMessages per receive, 1 to 10. Defaults to 10.
	MaxMessages int `yaml:"max_messages,omitempty"`

	// VisibilityTimeout is how long a received message stays hidden while it is handled. Defaults to the queue's own setting.
	VisibilityTimeout time.Duration `yaml:"visibility_timeout,omitempty"`
}

// Validate checks the settings and applies defaults.
func (s *SQSSource) Validate() []error {
	errs := s.AWSAccess.validate("the sqs source")
	if !strings.Contains(s.QueueURL, "://") {
		errs = append(errs, errors.New("sqs.queue_url is required, e.g. https://sqs.eu-west-2.amazonaws.com/123456789012/adt"))
	}
	if s.WaitSeconds == 0 {
		s.WaitSeconds = 20
	}
	if s.WaitSeconds < 1 || s.WaitSeconds > 20 {
		errs = append(errs, errors.New("sqs.wait_seconds must be 1 to 20, which is what SQS allows"))
	}
	if s.MaxMessages == 0 {
		s.MaxMessages = 10
	}
	if s.MaxMessages < 1 || s.MaxMessages > 10 {
		errs = append(errs, errors.New("sqs.max_messages must be 1 to 10, which is what SQS allows"))
	}
	if s.VisibilityTimeout < 0 || s.VisibilityTimeout > 12*time.Hour {
		errs = append(errs, errors.New("sqs.visibility_timeout must be between 0 and 12h, which is what SQS allows"))
	}
	return errs
}

// S3 source settings for what happens to an object after it is read.
const (
	S3AfterReadMove   = "move"
	S3AfterReadDelete = "delete"
)

// S3Source picks objects up from a bucket.
type S3Source struct {
	AWSAccess `yaml:",inline"`

	// Bucket is the bucket to read from.
	Bucket string `yaml:"bucket"`
	// PathStyle addresses the bucket as a path, which most S3-compatible stores need.
	PathStyle bool `yaml:"path_style,omitempty"`

	// Prefix limits which objects are read, e.g. inbound/. Empty reads the whole bucket, which is refused with after_read: move
	// unless move_to is outside it - otherwise moved objects would be read again.
	Prefix string `yaml:"prefix,omitempty"`

	// Suffix limits by ending, e.g. .hl7.
	Suffix string `yaml:"suffix,omitempty"`

	// AfterRead is move (the default) or delete. There is no leave: a bucket has no file a poller can remember having seen across a
	// restart, so leaving objects means reading them all again every time Perfuse starts.
	AfterRead string `yaml:"after_read,omitempty"`

	// MoveTo is the prefix a handled object moves to. Defaults to processed/.
	MoveTo string `yaml:"move_to,omitempty"`

	// ErrorPrefix is where an object that could not be handled moves, so one bad file does not block the rest. Defaults to error/.
	ErrorPrefix string `yaml:"error_prefix,omitempty"`

	// PollInterval between listings. Defaults to 30s.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`

	// MaxObjectSize refuses anything larger. Defaults to 16 MiB.
	MaxObjectSize int64 `yaml:"max_object_size,omitempty"`

	// Framed reads several MLLP-framed messages from one object. Otherwise an object is one message.
	Framed bool `yaml:"framed,omitempty"`
}

// Validate checks the settings and applies defaults.
func (s *S3Source) Validate() []error {
	errs := s.AWSAccess.validate("the s3 source")
	if strings.TrimSpace(s.Bucket) == "" {
		errs = append(errs, errors.New("s3.bucket is required"))
	}
	if strings.TrimSpace(s.Prefix) == "" || !strings.HasSuffix(s.Prefix, "/") {
		// Required, because handled objects move to processed/ and failed ones to error/ in the same bucket: reading the whole bucket
		// would read them again.
		errs = append(errs, errors.New("s3.prefix is required and ends in /, e.g. inbound/, so that processed/ and error/ are "+
			"outside what is read"))
	}
	if s.AfterRead == "" {
		s.AfterRead = S3AfterReadMove
	}
	if s.MoveTo == "" {
		s.MoveTo = "processed/"
	}
	if s.ErrorPrefix == "" {
		s.ErrorPrefix = "error/"
	}
	switch s.AfterRead {
	case S3AfterReadMove:
		if s.Prefix != "" && strings.HasPrefix(s.MoveTo, s.Prefix) {
			errs = append(errs, fmt.Errorf("s3.move_to %q is inside s3.prefix %q, so every moved object would be read again", s.MoveTo,
				s.Prefix))
		}
	case S3AfterReadDelete:
	default:
		errs = append(errs, fmt.Errorf("s3.after_read %q is not valid; use move or delete", s.AfterRead))
	}
	if s.Prefix != "" && strings.HasPrefix(s.ErrorPrefix, s.Prefix) {
		errs = append(errs, fmt.Errorf("s3.error_prefix %q is inside s3.prefix %q, so a failed object would be retried forever",
			s.ErrorPrefix, s.Prefix))
	}
	if s.PollInterval == 0 {
		s.PollInterval = 30 * time.Second
	}
	if s.PollInterval < time.Second {
		errs = append(errs, errors.New("s3.poll_interval must be at least 1s; each poll is a billed LIST request"))
	}
	if s.MaxObjectSize == 0 {
		s.MaxObjectSize = 16 << 20
	}
	if s.Endpoint != "" && !s.PathStyle && !strings.Contains(s.Endpoint, "amazonaws.com") {
		errs = append(errs, errors.New("s3.endpoint is not AWS, so it almost certainly needs s3.path_style: true"))
	}
	return errs
}

// SQSDestination sends each message to an SQS queue.
type SQSDestination struct {
	AWSAccess `yaml:",inline"`

	// QueueURL names the queue. One ending .fifo is a FIFO queue, and is grouped and deduplicated.
	QueueURL string `yaml:"queue_url"`

	// GroupBy decides a FIFO queue's MessageGroupId, which is what SQS keeps in order: patient (PID-3.1, the default), channel, or a
	// message path such as MSH-4. Messages for one patient stay in order; different patients proceed in parallel.
	GroupBy string `yaml:"group_by,omitempty"`
}

// Validate checks the settings.
func (s *SQSDestination) Validate() []error {
	errs := s.AWSAccess.validate("the sqs destination")
	if !strings.Contains(s.QueueURL, "://") {
		errs = append(errs, errors.New("sqs.queue_url is required, e.g. https://sqs.eu-west-2.amazonaws.com/123456789012/adt"))
	}
	if s.GroupBy == "" {
		s.GroupBy = "patient"
	}
	return errs
}

// SNSDestination publishes each message to an SNS topic.
type SNSDestination struct {
	AWSAccess `yaml:",inline"`

	// TopicARN names the topic, e.g. arn:aws:sns:eu-west-2:123456789012:adt.
	TopicARN string `yaml:"topic_arn"`

	// Subject is sent with email subscriptions. Optional; ${message_type} and ${control_id} are filled in.
	Subject string `yaml:"subject,omitempty"`

	// GroupBy is as for SQS, for a FIFO topic.
	GroupBy string `yaml:"group_by,omitempty"`
}

// Validate checks the settings.
func (s *SNSDestination) Validate() []error {
	errs := s.AWSAccess.validate("the sns destination")
	if !strings.HasPrefix(s.TopicARN, "arn:") {
		errs = append(errs, errors.New("sns.topic_arn is required, e.g. arn:aws:sns:eu-west-2:123456789012:adt"))
	}
	if s.GroupBy == "" {
		s.GroupBy = "patient"
	}
	return errs
}
