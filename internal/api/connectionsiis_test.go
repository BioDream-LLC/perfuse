package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/iis"
)

func TestAQuietRegistryIsAskedConnectivityTestAndNothingIsSubmitted(t *testing.T) {
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actions = append(actions, r.Header.Get("Content-Type"))
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope"><soap:Body>`+
			iis.ResponseBody("connectivityTest", iis.ConnectivityAnswer("perfuse"))+`</soap:Body></soap:Envelope>`)
	}))
	defer srv.Close()

	c := &connectionCheck{
		TCP: layer{State: "ok"}, App: layer{State: "skipped"}, Verdict: "ok",
		dest: config.Destination{Name: "iis", Type: config.DestinationSOAP, SOAP: &config.SOAPDestination{
			URL: srv.URL, Profile: iis.Profile, Version: "1.2", Username: "u", Password: "p"}},
	}
	registryEcho(context.Background(), c)

	if c.App.State != "ok" || !strings.Contains(c.App.Detail, "not the username and password") {
		t.Fatalf("got %+v", c.App)
	}
	if len(actions) != 1 || !strings.Contains(actions[0], iis.ActionConnectivity) {
		t.Fatalf("the probe called %v; it must call connectivityTest only", actions)
	}
}
