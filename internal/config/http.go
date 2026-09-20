package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// HTTPDestination posts messages to an HTTP endpoint.
//
// The commonest thing a Mirth channel does that Perfuse could not, and the
// commonest blocker the translator reports. Most uses are unglamorous: post the
// message to an internal API, post it to a web service that wraps a legacy system,
// post it to something somebody wrote in an afternoon.
type HTTPDestination struct {
	// URL is where to post. Required.
	URL string `yaml:"url"`

	// Method defaults to POST. PUT is allowed for an endpoint that wants it.
	//
	// GET and DELETE are refused: this sends a message body, and a transport that
	// silently dropped the message because of a method choice would be a very quiet
	// way to lose data.
	Method string `yaml:"method,omitempty"`

	// ContentType defaults to application/hl7-v2+er7, which is the registered type
	// for a pipe-delimited HL7 v2 message. Many endpoints want text/plain instead.
	ContentType string `yaml:"content_type,omitempty"`

	// Headers are added to every request, for an API key or a tenant selector.
	Headers map[string]string `yaml:"headers,omitempty"`

	// BearerToken is sent as an Authorization header. Prefer an environment
	// variable reference over a literal in a file that goes into git.
	BearerToken string `yaml:"bearer_token,omitempty"`

	// Username and Password use HTTP basic authentication, which a surprising
	// number of hospital systems still expect.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`

	// TLS configures client certificates and verification for an https URL.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`

	// SuccessStatus lists the status codes that count as delivered. Defaults to
	// any 2xx.
	//
	// Configurable because endpoints disagree about what success looks like: some
	// answer 200 with an error in the body, and some answer 202 for a queue they
	// have accepted the message into.
	SuccessStatus []int `yaml:"success_status,omitempty"`

	// FailOnBody treats a response containing any of these strings as a failure,
	// whatever the status code.
	//
	// This exists because an endpoint answering 200 with "ERROR: patient not found"
	// in the body is common, and treating it as delivered means the message is gone
	// and nobody knows.
	FailOnBody []string `yaml:"fail_on_body,omitempty"`

	// FollowRedirects allows the request to be redirected. Off by default: a
	// redirect on a write would repost clinical data somewhere the configuration
	// never named.
	FollowRedirects bool `yaml:"follow_redirects,omitempty"`
}

// HTTPMethod returns the method to use, defaulted.
func (h *HTTPDestination) HTTPMethod() string {
	if h == nil || h.Method == "" {
		return "POST"
	}
	return strings.ToUpper(h.Method)
}

// Type returns the content type to send, defaulted.
func (h *HTTPDestination) Type() string {
	if h == nil || h.ContentType == "" {
		return "application/hl7-v2+er7"
	}
	return h.ContentType
}

// Accepts reports whether a status code counts as success.
func (h *HTTPDestination) Accepts(status int) bool {
	if h == nil || len(h.SuccessStatus) == 0 {
		return status >= 200 && status < 300
	}
	for _, want := range h.SuccessStatus {
		if want == status {
			return true
		}
	}
	return false
}

func (h *HTTPDestination) validate() []error {
	if h == nil {
		return []error{errors.New("an http destination needs an http block with a url")}
	}

	var errs []error

	if strings.TrimSpace(h.URL) == "" {
		errs = append(errs, errors.New("http.url is required"))
	} else {
		u, err := url.Parse(h.URL)
		switch {
		case err != nil || u.Scheme == "" || u.Host == "":
			errs = append(errs, fmt.Errorf("http.url %q is not an absolute URL", h.URL))
		case u.Scheme != "http" && u.Scheme != "https":
			errs = append(errs, fmt.Errorf("http.url scheme %q is not http or https", u.Scheme))
		case u.Scheme == "http" && !isLocalHost(u.Hostname()):
			// A message body is patient data. Sending it unencrypted to a remote host
			// is a decision, not a default.
			errs = append(errs, fmt.Errorf(
				"http.url uses plain http to %s: the message would cross the network "+
					"unencrypted; use https", u.Hostname()))
		}
	}

	switch h.HTTPMethod() {
	case "POST", "PUT", "PATCH":
	case "GET", "DELETE", "HEAD":
		errs = append(errs, fmt.Errorf(
			"http.method %s does not send a body, so the message would be silently "+
				"discarded; use POST, PUT or PATCH", h.HTTPMethod()))
	default:
		errs = append(errs, fmt.Errorf("http.method %q is not a method", h.Method))
	}

	for _, status := range h.SuccessStatus {
		if status < 100 || status > 599 {
			errs = append(errs, fmt.Errorf("http.success_status %d is not a status code", status))
		}
	}

	if h.BearerToken != "" && h.Username != "" {
		// Sending both would mean one of them is ignored, and which one depends on
		// header ordering nobody should have to think about.
		errs = append(errs, errors.New(
			"http sets both bearer_token and username; use one form of authentication"))
	}
	if h.Username != "" && h.Password == "" {
		errs = append(errs, errors.New("http.username is set without a password"))
	}

	errs = append(errs, h.TLS.Validate(false)...)

	return errs
}

// HTTPSource accepts messages over HTTP.
//
// A listener rather than a poller: something posts a message and gets an
// acknowledgement back, which is how the modern half of the hospital integrates
// when MLLP is not available to it.
type HTTPSource struct {
	// Listen is the address to serve on. Required.
	Listen string `yaml:"listen"`

	// Path is the URL path to accept messages on. Defaults to "/".
	Path string `yaml:"path,omitempty"`

	// TLS encrypts inbound connections and can require a client certificate.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`

	// Token, when set, is required in an Authorization: Bearer header.
	//
	// There is no default and no anonymous mode toggle: an HTTP endpoint on a
	// hospital network with no authentication at all is a decision that has to be
	// visible in the file, so leaving this empty is reported as a warning at load.
	Token string `yaml:"token,omitempty"`

	// MaxMessageSize bounds an inbound body. Zero applies a default.
	MaxMessageSize int `yaml:"max_message_size,omitempty"`

	// ReadTimeout bounds reading a request.
	ReadTimeout time.Duration `yaml:"read_timeout,omitempty"`

	// Ack controls the acknowledgement returned in the response body.
	Ack Ack `yaml:"ack,omitempty"`
}

// HTTPPath returns the path to serve, defaulted.
func (h *HTTPSource) HTTPPath() string {
	if h == nil || h.Path == "" {
		return "/"
	}
	if !strings.HasPrefix(h.Path, "/") {
		return "/" + h.Path
	}
	return h.Path
}

func (h *HTTPSource) validate() []error {
	if h == nil {
		return []error{errors.New("an http source needs an http block with a listen address")}
	}

	var errs []error

	if strings.TrimSpace(h.Listen) == "" {
		errs = append(errs, errors.New("http.listen is required, for example \"127.0.0.1:8661\""))
	} else if err := validateListenAddr(h.Listen); err != nil {
		errs = append(errs, fmt.Errorf("http.listen: %w", err))
	}

	if h.MaxMessageSize < 0 {
		errs = append(errs, errors.New("http.max_message_size must not be negative"))
	}
	if h.ReadTimeout < 0 {
		errs = append(errs, errors.New("http.read_timeout must not be negative"))
	}

	errs = append(errs, h.TLS.Validate(true)...)

	return errs
}

// Warnings reports things that will work and should be said out loud.
func (h *HTTPSource) Warnings() []string {
	if h == nil {
		return nil
	}
	var out []string

	if h.Token == "" {
		// Said at every start rather than once, because an unauthenticated endpoint
		// accepting clinical messages is the kind of thing that gets set up for a
		// test and then forgotten.
		out = append(out, "this HTTP listener has no token, so anything that can reach "+
			"the address can submit messages. Set http.token, or restrict access at the "+
			"network layer and accept that risk deliberately")
	}
	if !h.TLS.IsEnabled() && !isLocalHost(hostOf(h.Listen)) {
		out = append(out, "this HTTP listener is not using TLS on a non-loopback "+
			"address, so submitted messages and any token cross the network in clear text")
	}

	return out
}

func hostOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}
