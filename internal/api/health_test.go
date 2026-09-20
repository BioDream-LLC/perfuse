package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func decodeHealth(t *testing.T, body []byte) healthResponse {
	t.Helper()
	var got healthResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding health response: %v\n%s", err, body)
	}
	return got
}

func TestLivenessIsAliveBeforeReadiness(t *testing.T) {
	h := newHarness(t)

	// Liveness must answer before MarkReady, or an orchestrator kills the process
	// during its own startup - the restart loop that looks like a crash and is
	// actually a misconfigured probe.
	rec := h.do("", http.MethodGet, "/livez", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("livez during startup = %d, want 200", rec.Code)
	}
	if got := decodeHealth(t, rec.Body.Bytes()).Status; got != "alive" {
		t.Errorf("status = %q", got)
	}
}

func TestReadinessIsFalseUntilMarkedReady(t *testing.T) {
	h := newHarness(t)

	rec := h.do("", http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before MarkReady = %d, want 503", rec.Code)
	}
	got := decodeHealth(t, rec.Body.Bytes())
	if got.Checks["lifecycle"] != "starting" {
		t.Errorf("lifecycle = %q, want starting", got.Checks["lifecycle"])
	}
}

func TestReadinessBecomesTrue(t *testing.T) {
	h := newHarness(t)
	h.server.MarkReady()

	rec := h.do("", http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz after MarkReady = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	got := decodeHealth(t, rec.Body.Bytes())
	if got.Status != "ready" {
		t.Errorf("status = %q", got.Status)
	}
	if got.Checks["database"] != "ok" {
		t.Errorf("database check = %q", got.Checks["database"])
	}
}

func TestDrainingMakesReadinessFalseButKeepsLivenessTrue(t *testing.T) {
	// This is the whole point of having two probes. On SIGTERM the instance must
	// stop receiving new traffic while remaining alive long enough to finish what
	// it already accepted. If liveness also failed, the supervisor would kill it
	// mid-message.
	h := newHarness(t)
	h.server.MarkReady()
	h.server.BeginDraining()

	if rec := h.do("", http.MethodGet, "/livez", nil); rec.Code != http.StatusOK {
		t.Errorf("livez while draining = %d, want 200 - a draining process must not be killed", rec.Code)
	}

	rec := h.do("", http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz while draining = %d, want 503", rec.Code)
	}
	// Said distinctly from a failure, because "shutting down as instructed" and
	// "broken" need different reactions from whoever is reading.
	if got := decodeHealth(t, rec.Body.Bytes()).Checks["lifecycle"]; got != "draining" {
		t.Errorf("lifecycle = %q, want draining", got)
	}
}

func TestDrainingIsReportedBeforeStartupCompletes(t *testing.T) {
	// A SIGTERM arriving during startup should still report draining rather than
	// starting, or a deploy that is being cancelled looks like one still coming up.
	h := newHarness(t)
	h.server.BeginDraining()

	rec := h.do("", http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", rec.Code)
	}
	if got := decodeHealth(t, rec.Body.Bytes()).Checks["lifecycle"]; got != "draining" {
		t.Errorf("lifecycle = %q, want draining", got)
	}
}

func TestProbesNeedNoCredentials(t *testing.T) {
	// A kubelet has no session. If these required auth the probe would fail closed
	// and the pod would never join the service.
	h := newHarness(t)
	h.server.MarkReady()

	for _, path := range []string{"/livez", "/readyz"} {
		rec := h.do("", http.MethodGet, path, nil)
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s returned 401; probes must not require credentials", path)
		}
	}
}

func TestReadinessReportsTheVersion(t *testing.T) {
	// During a rollback, which build is answering is the only question anybody has.
	h := newHarness(t)
	h.server.Version = "v1.2.3"
	h.server.MarkReady()

	rec := h.do("", http.MethodGet, "/readyz", nil)
	if got := decodeHealth(t, rec.Body.Bytes()).Version; got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", got)
	}
}

func TestDrainingIsVisibleToTheProcess(t *testing.T) {
	h := newHarness(t)
	if h.server.Draining() {
		t.Fatal("a fresh server reports draining")
	}
	h.server.BeginDraining()
	if !h.server.Draining() {
		t.Error("BeginDraining did not take effect")
	}
}
