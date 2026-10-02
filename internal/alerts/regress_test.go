package alerts

import (
	"testing"
	"time"
)

// TestRulesFromYAMLParseDurations guards the thing a live test caught: a rules
// file that loads without error but does not behave as written.
func TestQueueAgeRuleFiresOnASecondEvaluation(t *testing.T) {
	e := NewEvaluator([]Rule{{
		Kind: KindQueueAge, Threshold: 5, For: 5 * time.Second, Severity: Critical,
	}}, nil)

	start := time.Now()
	r := reading(start)
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 4, OldestSeconds: 98,
	}

	if got := len(e.Evaluate(r)); got != 0 {
		t.Fatalf("first evaluation should not fire, got %d", got)
	}

	r.At = start.Add(30 * time.Second)
	if got := len(e.Evaluate(r)); got != 1 {
		t.Fatalf("second evaluation 30s later should fire a 5s rule, got %d", got)
	}
}
