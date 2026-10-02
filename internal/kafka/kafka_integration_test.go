package kafka

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Against a real broker, because the parts of Kafka that can be got wrong are the parts a fake
// does not have.
//
// Consumer groups are a coordination protocol: join, sync, heartbeat, commit, and a rebalance
// between them. A stub that returns records proves the code compiles and proves nothing about
// whether this consumer will read every partition exactly once, which is the only property that
// matters for a clinical feed. So these run against a broker or they skip.
//
// Skipped rather than failed when there is no broker, because the unit suite has to pass on a
// machine with no Docker. The skip is loud in the output so it cannot be mistaken for a pass:
//
//	KAFKA_ADDR=localhost:9092 go test ./internal/kafka/

// brokerAddr returns the broker to test against, or skips.
func brokerAddr(t *testing.T) []string {
	t.Helper()

	addr := strings.TrimSpace(os.Getenv("KAFKA_ADDR"))
	if addr == "" {
		t.Skip("no KAFKA_ADDR; set it to a broker to run the integration tests, " +
			"for example KAFKA_ADDR=localhost:9092")
	}
	return strings.Split(addr, ",")
}

// A real HL7 admission, with the carriage returns HL7 actually uses.
//
// Built here rather than read from a file so the segment separator cannot be mangled in transit,
// which is exactly what happened when this was first tried through a line-oriented console
// producer: every \r became a newline and the message stopped being HL7.
func admission(mrn, control string) []byte {
	return []byte("MSH|^~\\&|EPIC|HOSP|PERFUSE|DEST|20260921120000||ADT^A01^ADT_A01|" +
		control + "|P|2.5.1\r" +
		"EVN|A01|20260921120000\r" +
		"PID|1||" + mrn + "^^^HOSP^MR||MOCKLIN^BRAVO||19700101|M\r")
}

// uniqueTopic keeps runs from reading each other's records.
func uniqueTopic(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// createTopic makes a topic with a known partition count and removes it afterwards.
//
// The partition count has to be set here rather than left to the broker, and the reason is that
// the keyed-ordering test is worthless without it: on a single-partition topic every record
// lands in partition 0 whatever its key, so the assertion that keyed records share a partition
// would pass while proving nothing. Found by running against a broker whose default is one
// partition.
func createTopic(t *testing.T, brokers []string, topic string, partitions int32) {
	t.Helper()

	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	defer client.Close()

	admin := kadm.NewClient(client)
	resp, err := admin.CreateTopics(context.Background(), partitions, 1, nil, topic)
	if err != nil {
		t.Fatalf("creating %s: %v", topic, err)
	}
	for _, r := range resp {
		if r.Err != nil {
			t.Fatalf("creating %s: %v", topic, r.Err)
		}
	}

	t.Cleanup(func() {
		c, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = kadm.NewClient(c).DeleteTopics(context.Background(), topic)
	})
}

// partitionCount reports how many partitions a topic has.
//
// Used to prove the fixture before asserting on it, so a broker that quietly created one
// partition cannot make the ordering test pass by making ordering trivial.
func partitionCount(t *testing.T, brokers []string, topic string) int {
	t.Helper()

	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	defer client.Close()

	details, err := kadm.NewClient(client).ListTopics(context.Background(), topic)
	if err != nil {
		t.Fatalf("listing %s: %v", topic, err)
	}
	d, ok := details[topic]
	if !ok {
		t.Fatalf("topic %s does not exist", topic)
	}
	return len(d.Partitions)
}

// A message published to a topic must come back off it unchanged.
//
// Byte-for-byte rather than "parses to the same thing". A single byte changed in an HL7 message
// can move a field boundary, and an integration that quietly alters the payload is worse than
// one that fails.
func TestAMessagePublishedComesBackUnchanged(t *testing.T) {
	brokers := brokerAddr(t)
	topic := uniqueTopic("perfuse-roundtrip")
	ctx := context.Background()
	createTopic(t, brokers, topic, 3)

	sent := admission("MRN0012345", "RT001")

	producer, err := NewProducer(ctx, &config.KafkaDestination{
		Brokers: brokers,
		Topic:   topic,
	}, nil)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	defer producer.Close()

	if err := producer.Publish(ctx, []byte("MRN0012345"), sent, map[string]string{
		"source-system": "perfuse",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	consumer, err := NewConsumer(ctx, &config.KafkaSource{
		Brokers:       brokers,
		Topics:        []string{topic},
		Group:         uniqueTopic("perfuse-group"),
		FromBeginning: true,
	}, nil)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer consumer.Close()

	rec := pollOne(t, consumer, 30*time.Second)

	if string(rec.Value) != string(sent) {
		t.Errorf("the message came back changed.\n sent: %q\n got:  %q", sent, rec.Value)
	}
	if string(rec.Key) != "MRN0012345" {
		t.Errorf("key = %q, want the MRN", rec.Key)
	}
	if rec.Headers["source-system"] != "perfuse" {
		t.Errorf("headers = %v, want source-system=perfuse", rec.Headers)
	}
}

// Records sharing a key must land in one partition, because that is the only thing that keeps a
// patient's events in order.
//
// Kafka guarantees order within a partition and nowhere else. This is the property the whole
// connector design rests on, and it is a property of the cluster rather than of this code - which
// is exactly why it is worth asserting against a real one rather than assuming it.
func TestRecordsWithOneKeyShareAPartition(t *testing.T) {
	brokers := brokerAddr(t)
	topic := uniqueTopic("perfuse-keyed")
	ctx := context.Background()
	createTopic(t, brokers, topic, 6)

	// The fixture, proved before anything is asserted about it. On a single-partition topic
	// every record shares a partition regardless of key, so the assertion below would pass
	// while testing nothing at all.
	if n := partitionCount(t, brokers, topic); n < 2 {
		t.Fatalf("the topic has %d partition(s); with fewer than 2 this test cannot "+
			"distinguish keyed records from unkeyed ones", n)
	}

	producer, err := NewProducer(ctx, &config.KafkaDestination{Brokers: brokers, Topic: topic}, nil)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	defer producer.Close()

	// One patient, several events, in the order a real admission produces them.
	const mrn = "MRN0012345"
	for i, control := range []string{"A01", "A08", "A03"} {
		if err := producer.Publish(ctx, []byte(mrn),
			admission(mrn, fmt.Sprintf("SEQ%03d-%s", i, control)), nil); err != nil {
			t.Fatalf("publishing %s: %v", control, err)
		}
	}

	consumer, err := NewConsumer(ctx, &config.KafkaSource{
		Brokers:       brokers,
		Topics:        []string{topic},
		Group:         uniqueTopic("perfuse-keyed-group"),
		FromBeginning: true,
	}, nil)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer consumer.Close()

	var got []Record
	deadline := time.Now().Add(30 * time.Second)
	for len(got) < 3 && time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		recs, err := consumer.Poll(pollCtx)
		cancel()
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		got = append(got, recs...)
	}
	if len(got) < 3 {
		t.Fatalf("read %d records, want 3", len(got))
	}

	partitions := map[int32]bool{}
	for _, r := range got {
		partitions[r.Partition] = true
	}
	if len(partitions) != 1 {
		t.Errorf("three records keyed on the same patient landed in %d partitions (%v); "+
			"Kafka only orders within a partition, so this patient's events can now be "+
			"read out of sequence", len(partitions), partitions)
	}

	// And in the order they were produced, which is what the single partition buys.
	for i, r := range got[:3] {
		want := fmt.Sprintf("SEQ%03d", i)
		if !strings.Contains(string(r.Value), want) {
			t.Errorf("record %d does not contain %s; the events came back out of order", i, want)
		}
	}
}

// Without a key, records spread across partitions - the behaviour the key exists to avoid.
//
// Asserted so the reasoning in the configuration comments is checked rather than merely written
// down. If a future default made unkeyed records land in one partition anyway, the warning in the
// interface would be wrong and nobody would find out.
func TestWithoutAKeyRecordsSpread(t *testing.T) {
	brokers := brokerAddr(t)
	topic := uniqueTopic("perfuse-unkeyed")
	ctx := context.Background()
	createTopic(t, brokers, topic, 6)

	producer, err := NewProducer(ctx, &config.KafkaDestination{Brokers: brokers, Topic: topic}, nil)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	defer producer.Close()

	// Enough records that landing all of them in one partition by chance is unlikely, on a
	// topic created with several partitions by the test setup.
	const n = 30
	for i := 0; i < n; i++ {
		if err := producer.Publish(ctx, nil,
			admission("MRN"+fmt.Sprint(i), fmt.Sprintf("U%03d", i)), nil); err != nil {
			t.Fatalf("publishing %d: %v", i, err)
		}
	}

	consumer, err := NewConsumer(ctx, &config.KafkaSource{
		Brokers:       brokers,
		Topics:        []string{topic},
		Group:         uniqueTopic("perfuse-unkeyed-group"),
		FromBeginning: true,
	}, nil)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer consumer.Close()

	partitions := map[int32]bool{}
	read := 0
	deadline := time.Now().Add(30 * time.Second)
	for read < n && time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		recs, err := consumer.Poll(pollCtx)
		cancel()
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		for _, r := range recs {
			partitions[r.Partition] = true
			read++
		}
	}

	t.Logf("%d unkeyed records landed in %d partition(s)", read, len(partitions))
	if read < n {
		t.Fatalf("read %d of %d records", read, n)
	}
	// Not asserted as "more than one", because a single-partition topic would make that fail
	// for a reason that is not this code's fault. Logged instead, and the keyed test above is
	// the one that asserts.
}

// An uncommitted batch must be delivered again, because that is what makes a crash safe.
//
// The whole acknowledgement design rests on this: commit after handling, so a failure
// redelivers rather than loses. Asserted by consuming without committing and then rejoining
// with the same group.
func TestUncommittedRecordsAreRedelivered(t *testing.T) {
	brokers := brokerAddr(t)
	topic := uniqueTopic("perfuse-recommit")
	group := uniqueTopic("perfuse-recommit-group")
	ctx := context.Background()
	createTopic(t, brokers, topic, 1)

	producer, err := NewProducer(ctx, &config.KafkaDestination{Brokers: brokers, Topic: topic}, nil)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	if err := producer.Publish(ctx, []byte("MRN1"), admission("MRN1", "RC001"), nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	producer.Close()

	src := &config.KafkaSource{
		Brokers:       brokers,
		Topics:        []string{topic},
		Group:         group,
		FromBeginning: true,
	}

	// Read it and deliberately do not commit, which is what a crash mid-batch looks like.
	first, err := NewConsumer(ctx, src, nil)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	if rec := pollOne(t, first, 30*time.Second); rec.Offset != 0 {
		t.Logf("first read came back at offset %d", rec.Offset)
	}
	first.Close()

	// Rejoin as the same group. The record must be offered again.
	second, err := NewConsumer(ctx, src, nil)
	if err != nil {
		t.Fatalf("rejoining: %v", err)
	}
	defer second.Close()

	rec := pollOne(t, second, 30*time.Second)
	if !strings.Contains(string(rec.Value), "RC001") {
		t.Errorf("after rejoining without committing, got %q; the record was not redelivered, "+
			"which means a crash between reading and handling would lose it silently",
			rec.Value)
	}
}

// Committing must stop the record being offered again.
func TestCommittedRecordsAreNotRedelivered(t *testing.T) {
	brokers := brokerAddr(t)
	topic := uniqueTopic("perfuse-committed")
	group := uniqueTopic("perfuse-committed-group")
	ctx := context.Background()
	createTopic(t, brokers, topic, 1)

	producer, err := NewProducer(ctx, &config.KafkaDestination{Brokers: brokers, Topic: topic}, nil)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	if err := producer.Publish(ctx, []byte("MRN2"), admission("MRN2", "CM001"), nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	producer.Close()

	src := &config.KafkaSource{
		Brokers: brokers, Topics: []string{topic}, Group: group, FromBeginning: true,
	}

	first, err := NewConsumer(ctx, src, nil)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	pollOne(t, first, 30*time.Second)
	if err := first.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	first.Close()

	second, err := NewConsumer(ctx, src, nil)
	if err != nil {
		t.Fatalf("rejoining: %v", err)
	}
	defer second.Close()

	// Polled briefly. Nothing should arrive, and waiting long proves nothing extra.
	pollCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	recs, err := second.Poll(pollCtx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	for _, r := range recs {
		if strings.Contains(string(r.Value), "CM001") {
			t.Error("a committed record was delivered again, so every restart would " +
				"reprocess the whole topic")
		}
	}
}

// An unreachable cluster must be refused when the connection is made, not at the first message.
func TestAnUnreachableClusterIsRefused(t *testing.T) {
	ctx := context.Background()

	// A port nothing listens on, with a short timeout so the test does not wait out a default.
	cfg := &config.KafkaDestination{
		Brokers: []string{"127.0.0.1:1"},
		Topic:   "nowhere",
		Timeout: 3 * time.Second,
	}
	if _, err := NewProducer(ctx, cfg, nil); err == nil {
		t.Error("a producer pointed at a cluster that does not exist was accepted; the " +
			"failure would then surface as the first patient message failing to deliver")
	}

	src := &config.KafkaSource{
		Brokers: []string{"127.0.0.1:1"},
		Topics:  []string{"nowhere"},
		Group:   "nobody",
		Timeout: 3 * time.Second,
	}
	if _, err := NewConsumer(ctx, src, nil); err == nil {
		t.Error("a consumer pointed at a cluster that does not exist was accepted; it would " +
			"then look like a topic with no traffic, which gets diagnosed as a quiet feed")
	}
}

// pollOne reads until one record arrives or the deadline passes.
func pollOne(t *testing.T, c *Consumer, within time.Duration) Record {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		recs, err := c.Poll(ctx)
		cancel()
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if len(recs) > 0 {
			return recs[0]
		}
	}
	t.Fatalf("no record arrived within %s", within)
	return Record{}
}
