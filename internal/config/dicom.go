package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// DICOMSource receives imaging objects as a C-STORE service class provider.
//
// What a hospital points a modality or a PACS at. Mirth calls this the DICOM Listener and it does C-STORE only, so this is
// parity rather than a subset.
type DICOMSource struct {
	// Listen is the address to serve on. Required. 104 is the registered DICOM port; 11112 is the common alternative
	// where binding a privileged port is not possible.
	Listen string `yaml:"listen"`

	// AETitle is what this endpoint calls itself.
	//
	// Strongly recommended rather than required, and warned about when absent. A site's only access control on a DICOM
	// endpoint is frequently the AE title, so an endpoint that accepts any title accepts anything that can reach it.
	AETitle string `yaml:"ae_title,omitempty"`

	// AllowedCallingAE lists the AE titles permitted to connect. Empty accepts any caller.
	//
	// This is the access control DICOM actually has. A PACS is routinely configured to accept one named calling title
	// and reject everything else, and this end had no equivalent: the called title was checked - which is the name the
	// caller dials, so anybody can send it correctly - while who was calling was recorded in the log and otherwise
	// ignored.
	//
	// Empty still accepts anything, because a modality nobody listed is the normal state of a first installation and
	// refusing by default would break every deployment on day one. It warns instead, and the warning names the titles
	// that have connected so somebody can fill this in from what actually arrived.
	AllowedCallingAE []string `yaml:"allowed_calling_ae,omitempty"`

	// SOPClasses restricts what kinds of object to accept. Empty accepts any.
	//
	// Empty is a reasonable default here because this engine relays objects rather than interpreting them, so refusing an
	// unfamiliar class would refuse valid images for no benefit.
	SOPClasses []string `yaml:"sop_classes,omitempty"`

	// TransferSyntaxes restricts the encodings to accept, in preference order. Empty accepts the uncompressed three.
	TransferSyntaxes []string `yaml:"transfer_syntaxes,omitempty"`

	// MaxObjectBytes bounds one received object. Zero applies a default.
	MaxObjectBytes int `yaml:"max_object_bytes,omitempty"`

	// IdleTimeout closes an association that has gone silent. Zero means never, which is usually right - a modality holds
	// an association open between studies.
	IdleTimeout time.Duration `yaml:"idle_timeout,omitempty"`

	// TLS encrypts inbound associations and can require a client certificate.
	//
	// Worth stating plainly: Mirth needs its paid SSL Manager extension for this, and the community forks cannot ship it
	// at all. DICOM carries patient names in its metadata, so this is one of the places where being a rewrite rather than
	// a fork is a real advantage rather than a stylistic one.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

func (d *DICOMSource) validate() []error {
	if d == nil {
		return []error{fmt.Errorf("a dicom source needs a dicom block")}
	}

	var errs []error

	if strings.TrimSpace(d.Listen) == "" {
		errs = append(errs, fmt.Errorf("a dicom source needs a listen address, such as 0.0.0.0:11112"))
	}

	for _, title := range d.AllowedCallingAE {
		trimmed := strings.TrimSpace(title)
		if trimmed == "" {
			// Refused rather than skipped. A blank entry in an allowlist looks like it permits something, and
			// silently dropping it would leave somebody believing a modality was listed when it was not.
			errs = append(errs, fmt.Errorf("dicom.allowed_calling_ae contains an empty title; remove it, "+
				"because an empty entry permits nothing and reads as though it permits something"))
			continue
		}
		if len(trimmed) > 16 {
			errs = append(errs, fmt.Errorf("dicom.allowed_calling_ae has %q, which is %d characters; "+
				"the protocol limit is 16 and a longer title can never match", trimmed, len(trimmed)))
		}
	}

	if len(d.AETitle) > 16 {
		// Sixteen characters is the protocol limit, and a longer title would be silently truncated on the wire - so the
		// remote would be configured with a name this endpoint never actually presents.
		errs = append(errs, fmt.Errorf("the AE title %q is %d characters; DICOM allows 16, and a longer one is "+
			"truncated on the wire so the remote would be configured with a name this endpoint never sends",
			d.AETitle, len(d.AETitle)))
	}

	if d.MaxObjectBytes < 0 {
		errs = append(errs, fmt.Errorf("max_object_bytes cannot be negative"))
	}

	return errs
}

// Warnings are things worth saying at every start rather than refusing.
func (d *DICOMSource) Warnings() []string {
	if d == nil {
		return nil
	}

	var out []string

	if strings.TrimSpace(d.AETitle) == "" {
		out = append(out, "this DICOM endpoint has no ae_title, so it accepts an association from anything that can "+
			"reach it; an AE title is frequently the only access control a site has on imaging")
	}
	if len(d.AllowedCallingAE) == 0 {
		// Said out loud at every start, because the called title is not access control. It is the name a caller
		// dials, so anybody who knows it can send it - and knowing it takes one connection attempt.
		out = append(out, "this DICOM endpoint has no allowed_calling_ae, so any host that can reach the port may "+
			"push images into this channel; the called AE title does not restrict this, because it is the name "+
			"the caller dials rather than proof of who is calling")
	}
	if d.TLS == nil {
		out = append(out, "this DICOM endpoint is not encrypted, and DICOM carries patient names in its metadata")
	}

	return out
}

// DICOMDestination sends imaging objects as a C-STORE service class user.
//
// Mirth calls this the DICOM Sender.
type DICOMDestination struct {
	// Address is the remote host and port. Required.
	Address string `yaml:"address"`

	// CalledAE is what the remote calls itself, and CallingAE what this end calls itself.
	//
	// Both matter. Sites configure a PACS to accept one specific calling title and reject everything else, so a mismatch
	// is the commonest reason a first connection fails - and the failure looks like a network problem.
	CalledAE  string `yaml:"called_ae,omitempty"`
	CallingAE string `yaml:"calling_ae,omitempty"`

	// TransferSyntaxes restricts what to offer, in preference order. Empty offers the object's own encoding and then
	// implicit VR little endian as a fallback.
	TransferSyntaxes []string `yaml:"transfer_syntaxes,omitempty"`

	// Timeout bounds one store operation. Zero applies a default.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// TLS encrypts the connection.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

func validateDICOMDest(d *Destination) []error {
	cfg := d.DICOM
	if cfg == nil {
		return []error{fmt.Errorf("destination %q is a dicom destination with no dicom block", d.Name)}
	}

	var errs []error

	if strings.TrimSpace(cfg.Address) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs an address, such as pacs.hospital.internal:104", d.Name))
	}

	for _, pair := range []struct {
		name  string
		value string
	}{{"called_ae", cfg.CalledAE}, {"calling_ae", cfg.CallingAE}} {
		if len(pair.value) > 16 {
			errs = append(errs, fmt.Errorf("destination %q has a %s of %d characters; DICOM allows 16, and a longer "+
				"one is truncated on the wire so the remote would be matching against a name never sent",
				d.Name, pair.name, len(pair.value)))
		}
	}

	if strings.TrimSpace(cfg.CalledAE) == "" {
		// Refused rather than defaulted. A PACS that checks the called title against its own name will reject a
		// placeholder, and the rejection reads as an outage - whereas being asked for the name up front costs nothing.
		errs = append(errs, fmt.Errorf("destination %q needs called_ae, the name the remote answers to; most archives "+
			"reject an association addressed to anything else, and that rejection looks like an outage", d.Name))
	}

	return errs
}
