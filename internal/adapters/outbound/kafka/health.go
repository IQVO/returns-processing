package kafka

import (
	"context"
	"errors"
	"fmt"

	kafkago "github.com/segmentio/kafka-go"
)

// MetadataClient is the subset of *kafkago.Client the readiness check needs,
// so unit tests can substitute a fake.
type MetadataClient interface {
	Metadata(ctx context.Context, req *kafkago.MetadataRequest) (*kafkago.MetadataResponse, error)
}

// BrokerCheck reports whether Kafka is reachable: a broker answers a metadata
// request. That is all it checks. It deliberately does NOT look at topics:
// on the cluster the broker auto-creates every business topic on first write
// (the fleet convention, warehouse-infra terraform/kafka.tf), so this
// service's own output topics do not exist until it first publishes, and a
// not-ready pod receives no traffic and so could never be driven to publish.
// A missing consumed topic is not a readiness failure either: the consumers
// retry and log it.
//
// The request names NO topics (an empty, non-nil list: zero topics, not the
// null list that means "all topics"), so the answer is small and asking never
// creates a topic (kafka-go's Client.Metadata does not set
// AllowAutoTopicCreation).
type BrokerCheck struct {
	client MetadataClient
}

// NewBrokerCheck builds the check over brokers. It does not dial.
func NewBrokerCheck(brokers []string) *BrokerCheck {
	return NewBrokerCheckWithClient(&kafkago.Client{Addr: kafkago.TCP(brokers...)})
}

// NewBrokerCheckWithClient builds the check over an explicit client (a fake in
// tests).
func NewBrokerCheckWithClient(c MetadataClient) *BrokerCheck {
	return &BrokerCheck{client: c}
}

// Check returns nil when a broker answered a topic-less metadata request;
// otherwise an error. ctx bounds the round trip.
func (b *BrokerCheck) Check(ctx context.Context) error {
	resp, err := b.client.Metadata(ctx, &kafkago.MetadataRequest{Topics: []string{}})
	if err != nil {
		return fmt.Errorf("kafka metadata: %w", err)
	}
	if resp == nil || len(resp.Brokers) == 0 {
		return errors.New("kafka metadata: the broker answered without listing any broker")
	}
	return nil
}
