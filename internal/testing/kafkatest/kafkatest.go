// Package kafkatest boots ONE real Kafka broker through testcontainers per
// test binary. It never reads KAFKA_BROKERS, never hardcodes a broker address
// and never skips: a missing Docker is a test failure (fleet rule,
// TestKafkaIntegrationTestsUseTestcontainers).
package kafkatest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
)

var (
	once      sync.Once
	container *tckafka.KafkaContainer
	brokers   []string
	startErr  error
	seq       atomic.Int64
)

// Brokers starts the shared broker on first use and returns its addresses.
// The broker has topic auto-creation OFF so a test controls when a topic
// appears (the cluster broker auto-creates topics on first write, the fleet
// convention, and the service's writers ask for it). A test that needs a topic
// creates it explicitly (Topic / CreateTopic). The service never calls a
// topic-admin API.
func Brokers(t *testing.T) []string {
	t.Helper()
	once.Do(func() {
		ctx := context.Background()
		container, startErr = tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1",
			tckafka.WithClusterID("returns-processing-itest"),
			testcontainers.WithEnv(map[string]string{"KAFKA_AUTO_CREATE_TOPICS_ENABLE": "false"}))
		if startErr != nil {
			return
		}
		brokers, startErr = container.Brokers(ctx)
	})
	if startErr != nil {
		t.Fatalf("start kafka container: %v", startErr)
	}
	return brokers
}

// Topic creates a unique one-partition topic named "<prefix>.itest-<n>" and
// waits until it has a leader (CreateTopics returns before the broker is done),
// then returns its name.
func Topic(t *testing.T, prefix string) string {
	t.Helper()
	topic := fmt.Sprintf("%s.itest-%d-%d", prefix, time.Now().UnixNano(), seq.Add(1))
	CreateTopic(t, topic)
	return topic
}

// CreateTopic creates a one-partition topic and waits for its leader.
func CreateTopic(t *testing.T, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", Brokers(t)[0])
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.CreateTopics(kafkago.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if parts, err := conn.ReadPartitions(topic); err == nil && len(parts) == 1 && parts[0].Leader.ID != 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("topic %s never got a leader", topic)
}

// Message is a consumed or produced Kafka message in neutral terms, so test
// files need not touch the Kafka client themselves.
type Message struct {
	Key     string
	Value   []byte
	Headers map[string]string
}

// ReadN reads exactly n messages from the single partition of topic, from the
// start, and fails the test if they do not arrive within timeout.
func ReadN(t *testing.T, topic string, n int, timeout time.Duration) []Message {
	t.Helper()
	reader := kafkago.NewReader(kafkago.ReaderConfig{Brokers: Brokers(t), Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20})
	defer func() { _ = reader.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out := make([]Message, 0, n)
	for len(out) < n {
		m, err := reader.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("read message %d of %d from %s: %v", len(out)+1, n, topic, err)
		}
		headers := make(map[string]string, len(m.Headers))
		for _, h := range m.Headers {
			headers[h.Key] = string(h.Value)
		}
		out = append(out, Message{Key: string(m.Key), Value: m.Value, Headers: headers})
	}
	return out
}

// Shutdown terminates the shared broker; call it from TestMain.
func Shutdown() {
	if container != nil {
		_ = testcontainers.TerminateContainer(container)
	}
}

// Produce writes messages to topic (key and headers as given) and waits for
// the broker's acknowledgement.
func Produce(t *testing.T, topic string, msgs ...Message) {
	t.Helper()
	w := &kafkago.Writer{Addr: kafkago.TCP(Brokers(t)...), Topic: topic, Balancer: &kafkago.Hash{}, RequiredAcks: kafkago.RequireAll, BatchTimeout: 10 * time.Millisecond}
	defer func() { _ = w.Close() }()
	out := make([]kafkago.Message, len(msgs))
	for i, m := range msgs {
		out[i] = kafkago.Message{Key: []byte(m.Key), Value: m.Value}
		for k, v := range m.Headers {
			out[i].Headers = append(out[i].Headers, kafkago.Header{Key: k, Value: []byte(v)})
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A just-created topic can still be unknown to the broker that serves the
	// produce request even once its leader is visible, so retry that one
	// error until the context deadline.
	var err error
	for ctx.Err() == nil {
		if err = w.WriteMessages(ctx, out...); err == nil || !errors.Is(err, kafkago.UnknownTopicOrPartition) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("produce to %s: %v", topic, err)
	}
}

// CommittedOffset returns the offset the consumer group has committed on
// partition 0 of topic (-1 when it committed nothing yet): the next offset the
// group will read, so after N messages were handled and committed it is N.
func CommittedOffset(t *testing.T, group, topic string) int64 {
	t.Helper()
	client := &kafkago.Client{Addr: kafkago.TCP(Brokers(t)...), Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := client.OffsetFetch(ctx, &kafkago.OffsetFetchRequest{GroupID: group, Topics: map[string][]int{topic: {0}}})
	if err != nil {
		t.Fatalf("fetch the committed offset of %s on %s: %v", group, topic, err)
	}
	for _, p := range resp.Topics[topic] {
		if p.Partition == 0 {
			if p.Error != nil {
				t.Fatalf("committed offset of %s on %s: %v", group, topic, p.Error)
			}
			return p.CommittedOffset
		}
	}
	t.Fatalf("no partition 0 in the offset response for %s on %s", group, topic)
	return -1
}
