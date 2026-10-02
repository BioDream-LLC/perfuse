package metrics

import (
	"bytes"
	"strings"
	"testing"
)

// The delivery admission metrics must be registered, and must say enough to be actionable.
//
// These exist because of what a limit looks like from outside. When the engine holds a delivery back, the
// visible effect is a queue filling up - which is exactly what a slow receiver looks like. An operator who
// cannot tell those apart restarts the wrong thing, and the second restart is the support call.
//
// So the requirement is not only that the numbers are published. It is that reading them answers the
// question: is something at its limit, and did anything actually get turned away.
func TestDeliveryAdmissionMetricsAreRegistered(t *testing.T) {
	c := New(Options{})

	byName := map[string]Definition{}
	for _, d := range c.Definitions() {
		byName[d.Name] = d
	}

	for _, want := range []struct {
		name string
		kind Kind
	}{
		{DeliveriesInFlight, KindGauge},
		{DeliveriesWaiting, KindGauge},
		{DeliveriesRefused, KindCounter},
		{DeliveryLimit, KindGauge},
	} {
		got, ok := byName[want.name]
		if !ok {
			t.Errorf("%s is not registered, so nothing scraping this server can see it", want.name)
			continue
		}
		if got.Kind != want.kind {
			t.Errorf("%s is a %v, want %v", want.name, got.Kind, want.kind)
		}
		if got.Help == "" {
			t.Errorf("%s has no help text; a number with no explanation gets misread", want.name)
		}
	}

	// The limit is published per scope, because the total and the per-destination share are different
	// numbers and confusing them makes the other two unreadable.
	if got := byName[DeliveryLimit]; len(got.Labels) != 1 || got.Labels[0] != "scope" {
		t.Errorf("%s labels = %v, want [scope]", DeliveryLimit, got.Labels)
	}

	// The two gauges carry no labels: they are process-wide facts, and a per-channel breakdown would imply
	// the budget is per channel, which is the misunderstanding this design exists to prevent.
	for _, name := range []string{DeliveriesInFlight, DeliveriesWaiting} {
		if got := byName[name]; len(got.Labels) != 0 {
			t.Errorf("%s has labels %v; the budget is process-wide, not per channel", name, got.Labels)
		}
	}
}

// And they must actually accept values, so a typo in the polling loop is caught here rather than by an
// empty graph nobody notices.
func TestDeliveryAdmissionMetricsAcceptValues(t *testing.T) {
	c := New(Options{})

	c.Set(DeliveriesInFlight, 3)
	c.Set(DeliveriesWaiting, 1)
	c.Set(DeliveriesRefused, 7)
	c.Set(DeliveryLimit, 9984, "total")
	c.Set(DeliveryLimit, 256, "per_destination")

	// Read back through the Prometheus rendering, which is what a scraper actually sees. A value the
	// collector accepted but never renders is invisible where it matters.
	var out bytes.Buffer
	if err := c.WritePrometheus(&out); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()

	for _, name := range []string{DeliveriesInFlight, DeliveriesWaiting, DeliveriesRefused, DeliveryLimit} {
		if !strings.Contains(rendered, name) {
			t.Errorf("%s took a value but is not rendered for a scraper", name)
		}
	}
	// Both scopes of the limit, so a reader can see the ceiling and the per-destination share.
	if !strings.Contains(rendered, `scope="total"`) {
		t.Error("the total limit is not rendered with its scope label")
	}
	if !strings.Contains(rendered, `scope="per_destination"`) {
		t.Error("the per-destination limit is not rendered with its scope label")
	}
}
