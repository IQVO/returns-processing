package postgres

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/outbox"
)

// TestRelayFailureLogsNoEventDerivedLineBreaks: the send-failed and
// attempt-cap logs of the outbox relay must carry no line break from the
// row's event type and key or from the send error.
func TestRelayFailureLogsNoEventDerivedLineBreaks(t *testing.T) {
	rec := newCRLFRecorder()
	r := &OutboxRelay{logger: slog.New(rec), maxAttempts: 3}
	row := claimedRow{id: 1, row: outbox.Row{EventID: "id" + crlfForged, AggregType: "aggreg" + crlfForged}}
	msg := outbox.Message{Topic: "logsafe.topic", EventType: "logsafe.Evil" + crlfForged, Key: []byte("key" + crlfForged)}
	sendErr := errors.New("broker refused" + crlfForged)
	r.logFailure(context.Background(), row, msg, 1, sendErr)
	r.logFailure(context.Background(), row, msg, 3, sendErr)
	rec.assertNoLineBreaks(t, 2)
}
