package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/settings"
	"github.com/biodream-llc/perfuse/internal/umlink"
)

// pas278Flags say how PAS reaches the payer's utilization management system. Secrets are never flags, where ps would show them:
// each is named by an environment variable, or saved in the console's settings (kept with the other secrets, never shown again).
type pas278Flags struct {
	url, sender, receiver, payer, transport                  *string
	coreVersion, coreRuleVersion, coreSender, coreReceiver   *string
	user, passwordEnv, tokenEnv                              *string
	oauthTokenURL, oauthClientID, oauthSecretEnv, oauthScope *string
	cert, key, ca                                            *string
	sftp, sftpUser, sftpPasswordEnv, sftpKey, sftpKnownHosts *string
	sftpOutbound, sftpInbound                                *string
	sftpEvery                                                *time.Duration
	production                                               *bool
}

func addPAS278Flags(fset *flag.FlagSet) *pas278Flags {
	f := &pas278Flags{}
	f.url = fset.String("pas-278-url", "",
		"forward every PAS request to the payer's utilization management system as an X12 278 request sent to this URL "+
			"(by -pas-278-transport), and answer with its 278 response; later 278 responses go to Claim/$decide-278")
	f.transport = fset.String("pas-278-transport", "x12",
		"how the 278s reach the UM system: x12 (POSTed as application/edi-x12), core-soap (a CAQH CORE SOAP envelope), "+
			"core-mime (CAQH CORE HTTP MIME multipart), or sftp (batch files over -pas-278-sftp, answers collected from its inbound folder)")
	f.sender = fset.String("pas-278-sender", "PERFUSE", "ISA06/GS02 sender ID on the 278s sent to the UM system")
	f.receiver = fset.String("pas-278-receiver", "", "ISA08/GS03 receiver ID on the 278s sent to the UM system")
	f.payer = fset.String("pas-278-payer-id", "", "the UMO's payer ID (NM1*X3) when the request's insurer carries no identifier")
	f.production = fset.Bool("pas-278-production", false, "mark the 278s sent as production (ISA15 P) rather than test")
	f.tokenEnv = fset.String("pas-278-token-env", "",
		"name of an environment variable holding a bearer token for the UM system (the token itself is never a flag)")
	f.coreVersion = fset.String("pas-278-core-version", umlink.CORE220,
		"the CAQH CORE Connectivity Rule the UM system runs, for core-soap and core-mime: 2.2.0 (payload in the envelope) or "+
			"C4.0.0 (payload as an MTOM attachment; SOAP only)")
	f.coreRuleVersion = fset.String("pas-278-core-rule-version", "",
		"what to write in the CORE envelope's CORERuleVersion when the UM system's guide asks for something other than the "+
			"version itself (CMS esMD's sample says v2.2.0)")
	f.coreSender = fset.String("pas-278-core-sender", "",
		"the CORE envelope's SenderID, as the UM system's connectivity guide gives it (default: -pas-278-sender)")
	f.coreReceiver = fset.String("pas-278-core-receiver", "",
		"the CORE envelope's ReceiverID, as the UM system's connectivity guide gives it (default: -pas-278-receiver)")
	f.user = fset.String("pas-278-user", "",
		"user name for the UM system: CORE's UserName (WS-Security UsernameToken in SOAP), or HTTP Basic for x12")
	f.passwordEnv = fset.String("pas-278-password-env", "",
		"name of an environment variable holding the password for -pas-278-user (the password itself is never a flag)")
	f.oauthTokenURL = fset.String("pas-278-oauth-token-url", "",
		"an OAuth 2.0 token endpoint: a token got with the client-credentials grant is sent as a bearer token (CORE vC4.0.0's authorization)")
	f.oauthClientID = fset.String("pas-278-oauth-client-id", "", "the OAuth client ID for -pas-278-oauth-token-url")
	f.oauthSecretEnv = fset.String("pas-278-oauth-secret-env", "",
		"name of an environment variable holding the OAuth client secret (the secret itself is never a flag)")
	f.oauthScope = fset.String("pas-278-oauth-scope", "", "the scope asked for with the OAuth token, when the UM system wants one")
	f.cert = fset.String("pas-278-cert", "", "a PEM client certificate for X.509 mutual TLS with the UM system (CORE's authentication)")
	f.key = fset.String("pas-278-key", "", "the private key (PEM) for -pas-278-cert")
	f.ca = fset.String("pas-278-ca", "", "a PEM file of certificate authorities to trust for the UM system beside the system's")
	f.sftp = fset.String("pas-278-sftp", "", "host:port of the UM system's SFTP server, for -pas-278-transport sftp")
	f.sftpUser = fset.String("pas-278-sftp-user", "", "the SFTP user name")
	f.sftpPasswordEnv = fset.String("pas-278-sftp-password-env", "",
		"name of an environment variable holding the SFTP password (the password itself is never a flag)")
	f.sftpKey = fset.String("pas-278-sftp-key", "", "an OpenSSH private key file for the SFTP user, instead of a password")
	f.sftpKnownHosts = fset.String("pas-278-sftp-known-hosts", "",
		"an OpenSSH known_hosts file holding the SFTP server's key; required, so requests never go to whoever answers")
	f.sftpOutbound = fset.String("pas-278-sftp-outbound", "", "the SFTP folder the 278 requests (and the 999s for the answers) are put in")
	f.sftpInbound = fset.String("pas-278-sftp-inbound", "", "the SFTP folder the UM system's 278 responses, 999s and TA1s are collected from")
	f.sftpEvery = fset.Duration("pas-278-sftp-every", time.Minute, "how often the SFTP inbound folder is read")
	return f
}

// enabled reports whether a UM system is configured.
func (f *pas278Flags) enabled() bool {
	return *f.url != "" || (*f.transport == umlink.TransportSFTP && *f.sftp != "")
}

// secret is the value of the environment variable a flag names, or else the secret saved in the console's settings.
func secret(env, key string, store *settings.Store) (string, error) {
	if env != "" {
		v := strings.TrimSpace(os.Getenv(env))
		if v == "" {
			return "", fmt.Errorf("the environment variable %s is empty", env)
		}
		return v, nil
	}
	if store == nil {
		return "", nil
	}
	return strings.TrimSpace(store.String(key)), nil
}

// link builds the connection to the UM system.
func (f *pas278Flags) link(store *settings.Store) (*umlink.Link, error) {
	cfg := umlink.Config{Transport: *f.transport, URL: *f.url, COREVersion: *f.coreVersion, CORERuleVersion: *f.coreRuleVersion,
		SenderID: *f.coreSender, ReceiverID: *f.coreReceiver, Username: *f.user, ClientCert: *f.cert, ClientKey: *f.key, CAFile: *f.ca,
		OAuth: umlink.OAuth{TokenURL: *f.oauthTokenURL, ClientID: *f.oauthClientID, Scope: *f.oauthScope},
		SFTP: umlink.SFTP{Address: *f.sftp, User: *f.sftpUser, KeyFile: *f.sftpKey, KnownHosts: *f.sftpKnownHosts,
			Outbound: *f.sftpOutbound, Inbound: *f.sftpInbound}}
	if cfg.SenderID == "" {
		cfg.SenderID = *f.sender
	}
	if cfg.ReceiverID == "" {
		cfg.ReceiverID = *f.receiver
	}
	var err error
	if cfg.Password, err = secret(*f.passwordEnv, "payers.pas278Password", store); err != nil {
		return nil, fmt.Errorf("-pas-278-password-env: %w", err)
	}
	if cfg.BearerToken, err = secret(*f.tokenEnv, "payers.pas278Token", store); err != nil {
		return nil, fmt.Errorf("-pas-278-token-env: %w", err)
	}
	if cfg.OAuth.ClientSecret, err = secret(*f.oauthSecretEnv, "payers.pas278OAuthSecret", store); err != nil {
		return nil, fmt.Errorf("-pas-278-oauth-secret-env: %w", err)
	}
	if cfg.SFTP.Password, err = secret(*f.sftpPasswordEnv, "payers.pas278SFTPPassword", store); err != nil {
		return nil, fmt.Errorf("-pas-278-sftp-password-env: %w", err)
	}
	if cfg.Username != "" && cfg.Password == "" {
		return nil, errors.New("-pas-278-user needs a password: -pas-278-password-env, or one saved in the console")
	}
	if cfg.Transport == umlink.TransportCOREMIME && cfg.COREVersion == umlink.CORE400 {
		return nil, errors.New("CAQH CORE vC4.0.0 has no MIME envelope: use -pas-278-transport core-soap with it")
	}
	return umlink.New(cfg)
}

// send adapts a link to PAS: a batch link's "answer later" is the error PAS reads as delivered.
func pas278Send(link *umlink.Link) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, request []byte) ([]byte, error) {
		out, err := link.Send(ctx, request)
		if errors.Is(err, umlink.ErrAnswerLater) {
			return nil, fhirserver.ErrUMAnswersLater
		}
		return out, err
	}
}

// pollPAS278 reads a batch link's inbound folder until ctx ends, handing each file to PAS.
func pollPAS278(ctx context.Context, link *umlink.Link, srv *fhirserver.Server, every time.Duration, log *slog.Logger) {
	if every < 5*time.Second {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		err := link.Poll(ctx, func(name string, body []byte) ([]byte, error) {
			reply, summary, err := srv.ApplyUMFile(ctx, body)
			if err != nil {
				return nil, err
			}
			log.Info("PAS: a file from the utilization management system was applied", "file", name, "summary", summary)
			return reply, nil
		})
		if err != nil {
			log.Warn("PAS: reading the utilization management system's SFTP folder", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
