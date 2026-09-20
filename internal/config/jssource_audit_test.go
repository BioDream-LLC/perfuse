package config

import (
	"strings"
	"testing"
)

// AUDIT ITEM 1: Unknown YAML keys in the jssource block must be REJECTED.
//
// A silently-ignored key means a user sets a timeout or permission and it has no effect. For a
// permission setting this is a security defect: restricting a script's capabilities via a
// misspelled key gives an unrestricted script.

func TestJavaScriptSourceLoads(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: javascript
  javascript:
    script: |
      return "hello";
    poll_interval: 10s
    timeout: 5s
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err != nil {
		t.Fatalf("a valid javascript source was rejected: %v", err)
	}
}

func TestJavaScriptSourceUnknownKeyRejected(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: javascript
  javascript:
    script: |
      return "hello";
    poll_interval: 10s
    tiemout: 5s
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("a misspelled key (tiemout) in the javascript source was silently accepted; " +
			"a user setting a timeout that has no effect gets an unrestricted script")
	}
	if !strings.Contains(err.Error(), "tiemout") {
		t.Errorf("error does not name the bad field: %v", err)
	}
}

func TestJavaScriptSourceUnknownPermissionKeyRejected(t *testing.T) {
	// A user thinking they can restrict permissions via the javascript block
	// would get an unrestricted script if the key is silently ignored.
	_, err := load(t, `
name: jstest
source:
  type: javascript
  javascript:
    script: |
      return "hello";
    permissions:
      - file
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("an unknown key 'permissions' in the javascript source block was silently accepted; " +
			"this is a security-relevant field that has no effect")
	}
	if !strings.Contains(err.Error(), "permissions") {
		t.Errorf("error does not name the bad field: %v", err)
	}
}

// AUDIT ITEM 2: Every field that gates a capability must be plumbed through to the engine.
// The fields are: Script, PollInterval, Timeout.

func TestJavaScriptSourceScriptIsRequired(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: javascript
  javascript:
    poll_interval: 5s
    timeout: 10s
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("a javascript source with an empty script was accepted; it would poll forever and produce nothing")
	}
}

func TestJavaScriptSourceBlockRequired(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: javascript
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("a javascript source with no javascript block was accepted")
	}
	if !strings.Contains(err.Error(), "javascript") {
		t.Errorf("error does not mention javascript: %v", err)
	}
}

// AUDIT ITEM 3: Defaults are sane. Default timeout must be non-zero.

func TestJavaScriptSourceDefaultTimeoutIsNonZero(t *testing.T) {
	if DefaultJSSourceTimeout == 0 {
		t.Fatal("default timeout is zero; a zero timeout meaning no limit lets a runaway script hang a channel forever")
	}
}

func TestJavaScriptSourceDefaultPollIntervalIsNonZero(t *testing.T) {
	if DefaultJSSourcePollInterval == 0 {
		t.Fatal("default poll interval is zero; a zero interval would spin-loop")
	}
}

// AUDIT ITEM 4: A malformed value is refused at load with a message naming the field.

func TestJavaScriptSourceNegativeTimeoutRejected(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: javascript
  javascript:
    script: |
      return "hello";
    timeout: -5s
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("a negative timeout was accepted")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error does not name the field: %v", err)
	}
}

func TestJavaScriptSourceNegativePollIntervalRejected(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: javascript
  javascript:
    script: |
      return "hello";
    poll_interval: -1s
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("a negative poll interval was accepted")
	}
	if !strings.Contains(err.Error(), "poll_interval") {
		t.Errorf("error does not name the field: %v", err)
	}
}

func TestJavaScriptSourceExcessiveTimeoutRejected(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: javascript
  javascript:
    script: |
      return "hello";
    timeout: 10m
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("a 10 minute timeout was accepted; this would stall a channel")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error does not name the field: %v", err)
	}
}

// A javascript block on a non-javascript source must be rejected.
func TestJavaScriptBlockOnWrongSourceTypeRejected(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: mllp
  listen: ":6661"
  javascript:
    script: |
      return "hello";
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("a javascript block on an mllp source was accepted; the script would never run")
	}
}

// source.listen should not apply to a javascript source.
func TestJavaScriptSourceListenRejected(t *testing.T) {
	_, err := load(t, `
name: jstest
source:
  type: javascript
  listen: ":6661"
  javascript:
    script: |
      return "hello";
destinations:
  - name: out
    type: file
    dir: /tmp/out
`)
	if err == nil {
		t.Fatal("source.listen on a javascript source was accepted; it polls rather than listens")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Errorf("error does not mention listen: %v", err)
	}
}
