package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Azure Blob Storage, as a destination and a source.
//
// Authorised with the storage account key (Shared Key) or, better, a SAS token limited to the container and the operations needed.
// Either may be a ${ENV_VAR} reference, which is how it should be supplied.

// AzureBlobAccess is the account, container and credential.
type AzureBlobAccess struct {
	// Account is the storage account name.
	Account string `yaml:"account"`
	// Container is the blob container.
	Container string `yaml:"container"`
	// Key is the account key, or ${AZURE_STORAGE_KEY}. Set it or sas.
	Key string `yaml:"key,omitempty"`
	// SAS is a shared access signature query string, or a ${...} reference. Preferred: it can be limited to this container.
	SAS string `yaml:"sas,omitempty"`
	// Endpoint overrides https://<account>.blob.core.windows.net, for Azurite or a private endpoint.
	Endpoint string `yaml:"endpoint,omitempty"`
}

// ResolvedKey expands an environment reference.
func (a *AzureBlobAccess) ResolvedKey() string { return resolveSecret(a.Key) }

// ResolvedSAS expands an environment reference.
func (a *AzureBlobAccess) ResolvedSAS() string { return resolveSecret(a.SAS) }

func (a *AzureBlobAccess) validate(what string) []error {
	var errs []error
	if strings.TrimSpace(a.Account) == "" || strings.TrimSpace(a.Container) == "" {
		errs = append(errs, fmt.Errorf("%s needs azure_blob.account and azure_blob.container", what))
	}
	if a.ResolvedKey() == "" && a.ResolvedSAS() == "" {
		errs = append(errs, fmt.Errorf("%s needs azure_blob.sas or azure_blob.key; reference the environment, e.g. ${AZURE_STORAGE_SAS}", what))
	}
	return errs
}

// AzureBlobDestination writes each message as a block blob.
type AzureBlobDestination struct {
	AzureBlobAccess `yaml:",inline"`
	// Blob names each blob, with the S3 destination's placeholders. Defaults to ${date}/${channel}/${control_id}.hl7.
	Blob string `yaml:"blob,omitempty"`
	// Tier is Hot, Cool, Cold or Archive. Archive is offline: reading a blob back means rehydrating it, which takes hours.
	Tier string `yaml:"tier,omitempty"`
	// ContentType defaults to application/hl7-v2.
	ContentType string `yaml:"content_type,omitempty"`
}

// Validate checks the settings.
func (d *AzureBlobDestination) Validate() []error {
	errs := d.validate("the azure_blob destination")
	if d.Tier != "" {
		ok := false
		for _, t := range []string{"Hot", "Cool", "Cold", "Archive"} {
			if strings.EqualFold(t, d.Tier) {
				d.Tier, ok = t, true
			}
		}
		if !ok {
			errs = append(errs, fmt.Errorf("azure_blob.tier %q is not Hot, Cool, Cold or Archive", d.Tier))
		}
	}
	if d.Blob == "" {
		d.Blob = defaultS3Key
	}
	return errs
}

// AzureBlobSource picks blobs up from a container prefix.
type AzureBlobSource struct {
	AzureBlobAccess `yaml:",inline"`
	// Prefix is required and ends in /, so processed/ and error/ are outside what is read.
	Prefix string `yaml:"prefix"`
	// Suffix limits by ending, e.g. .hl7.
	Suffix string `yaml:"suffix,omitempty"`
	// AfterRead is move (the default) or delete.
	AfterRead string `yaml:"after_read,omitempty"`
	// MoveTo is where a handled blob moves. Defaults to processed/.
	MoveTo string `yaml:"move_to,omitempty"`
	// ErrorPrefix is where a blob that cannot be handled moves, so it does not block the rest. Defaults to error/.
	ErrorPrefix string `yaml:"error_prefix,omitempty"`
	// PollInterval between listings. Defaults to 30s.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`
	// MaxObjectSize refuses a larger blob, which is left where it is. Defaults to 16 MiB.
	MaxObjectSize int64 `yaml:"max_object_size,omitempty"`
	// Framed reads several MLLP-framed messages from one blob, every one of which must be accepted before the blob is moved.
	Framed bool `yaml:"framed,omitempty"`
}

// Validate checks the settings and applies the same defaults as the S3 source.
func (s *AzureBlobSource) Validate() []error {
	errs := s.validate("the azure_blob source")
	if !strings.HasSuffix(s.Prefix, "/") {
		errs = append(errs, errors.New("azure_blob.prefix is required and ends in /, e.g. inbound/"))
	}
	if s.AfterRead == "" {
		s.AfterRead = S3AfterReadMove
	}
	if s.AfterRead != S3AfterReadMove && s.AfterRead != S3AfterReadDelete {
		errs = append(errs, fmt.Errorf("azure_blob.after_read %q is not valid; use move or delete", s.AfterRead))
	}
	if s.MoveTo == "" {
		s.MoveTo = "processed/"
	}
	if s.ErrorPrefix == "" {
		s.ErrorPrefix = "error/"
	}
	if s.Prefix != "" && (strings.HasPrefix(s.MoveTo, s.Prefix) || strings.HasPrefix(s.ErrorPrefix, s.Prefix)) {
		errs = append(errs, errors.New("azure_blob.move_to and error_prefix must be outside prefix, or blobs would be read again"))
	}
	if s.PollInterval == 0 {
		s.PollInterval = 30 * time.Second
	}
	if s.PollInterval < time.Second {
		errs = append(errs, errors.New("azure_blob.poll_interval must be at least 1s"))
	}
	if s.MaxObjectSize == 0 {
		s.MaxObjectSize = 16 << 20
	}
	return errs
}
