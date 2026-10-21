package kafka

import (
	"context"
	"errors"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

type fakeMetadata struct {
	err  error
	resp *kafkago.MetadataResponse
}

func (f *fakeMetadata) Metadata(context.Context, *kafkago.MetadataRequest) (*kafkago.MetadataResponse, error) {
	return f.resp, f.err
}

func TestBrokerCheckPassesOnABrokerAnswer(t *testing.T) {
	f := &fakeMetadata{resp: &kafkago.MetadataResponse{Brokers: []kafkago.Broker{{ID: 1}}}}
	if err := NewBrokerCheckWithClient(f).Check(context.Background()); err != nil {
		t.Fatalf("Check = %v", err)
	}
}

func TestBrokerCheckFailsOnATransportError(t *testing.T) {
	boom := errors.New("connection refused")
	if err := NewBrokerCheckWithClient(&fakeMetadata{err: boom}).Check(context.Background()); err == nil {
		t.Fatal("Check passed on a transport error")
	}
}

func TestBrokerCheckFailsOnABrokerlessAnswer(t *testing.T) {
	if err := NewBrokerCheckWithClient(&fakeMetadata{resp: &kafkago.MetadataResponse{}}).Check(context.Background()); err == nil {
		t.Fatal("Check passed on a broker-less answer")
	}
}

// The request must carry an EMPTY non-nil topic list: a nil list means "all
// topics" (a heavier answer), and a named topic would tie readiness to a
// topic's existence.
func TestBrokerCheckAsksForZeroTopicsNeverAll(t *testing.T) {
	var asked *kafkago.MetadataRequest
	rec := metadataRecorder{asked: &asked, resp: &kafkago.MetadataResponse{Brokers: []kafkago.Broker{{ID: 1}}}}
	if err := NewBrokerCheckWithClient(rec).Check(context.Background()); err != nil {
		t.Fatalf("Check = %v", err)
	}
	if asked == nil || asked.Topics == nil || len(asked.Topics) != 0 {
		t.Fatalf("metadata request topics = %+v, want an empty non-nil list", asked)
	}
}

type metadataRecorder struct {
	asked **kafkago.MetadataRequest
	resp  *kafkago.MetadataResponse
}

func (r metadataRecorder) Metadata(_ context.Context, req *kafkago.MetadataRequest) (*kafkago.MetadataResponse, error) {
	*r.asked = req
	return r.resp, nil
}

func TestNewBrokerCheckDoesNotDial(t *testing.T) {
	// Constructing the check over an unreachable address must not dial (the
	// probe dials on Check, under its own deadline).
	if NewBrokerCheck([]string{"127.0.0.1:1"}) == nil {
		t.Fatal("NewBrokerCheck returned nil")
	}
}
