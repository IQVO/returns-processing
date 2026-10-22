//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/returns-processing/internal/adapters/outbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/testing/kafkatest"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// The infrastructure comes from testcontainers, never from the environment.
func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

// The analytics read side end to end on real infrastructure: REAL domain
// events go through the transactional outbox (the production payload
// mapping), the relay drains them to BOTH topics (the integration topic and
// its analytics twin under one CloudEvents id), the projector's own consumer
// folds the analytics stream into a real analytical Postgres (migrated by
// the projector's boot step), a poison message lands on the DLQ, and the
// reports router the returns-reports binary serves answers from a READ-ONLY
// pool.
func TestAnalyticsReadSideFromTheRealOutboxToTheReports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	brokers := kafkatest.Brokers(t)

	// The OLTP side: the outbox table only.
	oltpURL := pgtest.EmptyURL(t)
	if err := postgres.RunMigrations(oltpURL); err != nil {
		t.Fatalf("oltp migrations: %v", err)
	}
	oltp := pgtest.Open(t, oltpURL)
	defer oltp.Close()

	// The projector's own boot step migrates the EMPTY analytical database.
	analyticsURL := pgtest.EmptyURL(t)
	analyticsPool, err := openAnalyticsPool(ctx, logger, analyticsURL)
	if err != nil {
		t.Fatalf("openAnalyticsPool: %v", err)
	}
	defer analyticsPool.Close()

	// The relay writes to the fleet topics; the test broker has
	// auto-creation OFF, so all three are created explicitly (the projector
	// dead-letters deterministic poison to the analytics DLQ).
	eventsTopic := outboundkafka.TopicEvents
	analyticsTopic := outboundkafka.TopicAnalytics
	kafkatest.CreateTopic(t, eventsTopic)
	kafkatest.CreateTopic(t, analyticsTopic)
	kafkatest.CreateTopic(t, analyticsTopic+inboundkafka.DLQSuffix)

	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay()
	relay := postgres.NewOutboxRelay(oltp, outboundkafka.NewEncoder(), outboundkafka.NewRelaySink(brokers),
		postgres.WithLogger(logger), postgres.WithInterval(100*time.Millisecond), postgres.WithMaxAttempts(20))
	go func() { _ = relay.Run(relayCtx) }()

	// One full return line lifecycle as REAL domain events (the golden
	// tests' shapes): opened, inspected, decided RESTOCK under policy
	// version 2, staged 6m30s after the receipt opened.
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	openedAt := day.Add(10 * time.Hour)
	opened := returnreceipt.ReturnReceiptOpened{
		Header: returnreceipt.Header{Receipt: "rcpt-an-1", Version: 2, At: openedAt},
		RMAID:  "rma-an-1", SiteID: "SITE-AN", Opened: openedAt,
		Lines: []returnreceipt.OpenedLine{{LineNo: 1, SKU: "SKU-AN", Quantity: 2, RMAID: "rma-an-1"}},
	}
	inspected := returnreceipt.ReturnLineInspected{
		Header: returnreceipt.Header{Receipt: "rcpt-an-1", Version: 3, At: openedAt.Add(5 * time.Minute)},
		LineNo: 1, SKU: "SKU-AN", Quantity: 2, Grade: "A",
		ReasonCodes: []string{"like-new"}, Inspected: openedAt.Add(5 * time.Minute),
	}
	decidedEv := disposition.DispositionDecided{
		Header:    disposition.Header{Disposition: "disp-an-1", Version: 1, At: openedAt.Add(6 * time.Minute)},
		ReceiptID: "rcpt-an-1", LineNo: 1, SKU: "SKU-AN", Quantity: 2, Grade: "A",
		Outcome: disposition.OutcomeRestock, PolicyVersion: 2, DecidedAt: openedAt.Add(6 * time.Minute),
	}
	stagedEv := disposition.ReturnLineStaged{
		Header:    disposition.Header{Disposition: "disp-an-1", Version: 2, At: openedAt.Add(390 * time.Second)},
		ReceiptID: "rcpt-an-1", LineNo: 1, SKU: "SKU-AN", Quantity: 2,
		Condition: disposition.StagedCondition, ReceivedAt: openedAt, RMAID: "rma-an-1", SiteID: "SITE-AN",
	}

	ob := postgres.NewOutbox(oltp)
	if err := ob.PublishReceiptEvents(ctx, opened, inspected); err != nil {
		t.Fatalf("publish receipt events: %v", err)
	}
	if err := ob.PublishDispositionEvents(ctx, decidedEv, stagedEv); err != nil {
		t.Fatalf("publish disposition events: %v", err)
	}

	// A known type whose payload contradicts its subject can never be
	// projected: the projector must dead-letter it (raw bytes, x-dlq-error).
	tamperedAt := openedAt.Add(7 * time.Minute)
	tamperedValue, err := cloudevents.New(cloudevents.Spec{
		ID: "ev-poison", Entity: "disposition", EventName: "ReturnLineStaged", Subject: "disp-OTHER",
		Time: tamperedAt, Stream: cloudevents.StreamAnalytics, Version: 1,
		Data: map[string]any{
			"receipt_id": "rcpt-an-1", "line_no": 1, "sku": "SKU-AN", "quantity": 2, "condition": "Good",
			"received_at": openedAt.Format(time.RFC3339), "rma_id": "rma-an-1", "disposition_id": "disp-an-1", "site_id": "SITE-AN",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	kafkatest.Produce(t, analyticsTopic, kafkatest.Message{Key: "disp-OTHER", Value: tamperedValue})

	// The relay drained the outbox to BOTH topics.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if pgtest.Count(t, oltp, "outbox") == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := pgtest.Count(t, oltp, "outbox"); n != 0 {
		t.Fatalf("the outbox still holds %d rows: the relay did not drain", n)
	}

	// The analytics twin of every occurrence: same id, same data, only the
	// dataschema stream differs (the poison message produced below for the
	// DLQ check shares the topic, so twins are matched by id, not position).
	integrationMsgs := kafkatest.ReadN(t, eventsTopic, 4, 30*time.Second)
	twinMsgs := kafkatest.ReadN(t, analyticsTopic, 5, 30*time.Second)
	twinsByID := map[string]json.RawMessage{}
	twinSchemaByID := map[string]string{}
	for _, m := range twinMsgs {
		var twin struct {
			ID         string          `json:"id"`
			DataSchema string          `json:"dataschema"`
			Data       json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(m.Value, &twin); err != nil {
			t.Fatalf("analytics message: %v: %s", err, m.Value)
		}
		twinsByID[twin.ID] = twin.Data
		twinSchemaByID[twin.ID] = twin.DataSchema
	}
	for i := range integrationMsgs {
		var got struct {
			ID         string          `json:"id"`
			DataSchema string          `json:"dataschema"`
			Data       json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(integrationMsgs[i].Value, &got); err != nil {
			t.Fatalf("integration message %d: %v", i, err)
		}
		data, ok := twinsByID[got.ID]
		if !ok {
			t.Fatalf("message %d (%s) has no analytics twin", i, got.ID)
		}
		if string(data) != string(got.Data) {
			t.Fatalf("twin %s data differs:\n twin: %s\n  evt: %s", got.ID, data, got.Data)
		}
		if schema := twinSchemaByID[got.ID]; !strings.Contains(schema, ":analytics:") || schema == got.DataSchema {
			t.Fatalf("twin %s dataschema = %s (integration %s)", got.ID, schema, got.DataSchema)
		}
	}

	// The projector's own consumer wiring, on the analytics topic.
	consumer := inboundkafka.NewAnalyticsConsumer(brokers, analyticsTopic, "returns-projector-itest-"+day.Format("150405"),
		analyticsstore.NewProjection(analyticsPool), logger)
	consumer.Retry = inboundkafka.RetryPolicy{Initial: 10 * time.Millisecond, Max: 50 * time.Millisecond}
	consumerCtx, stopConsumer := context.WithCancel(ctx)
	defer stopConsumer()
	consumerDone := make(chan error, 1)
	go func() { consumerDone <- consumer.Run(consumerCtx) }()
	t.Cleanup(func() {
		stopConsumer()
		select {
		case <-consumerDone:
		case <-time.After(10 * time.Second):
			t.Error("the analytics consumer did not stop")
		}
		_ = consumer.Close()
	})

	// The reports binary's router over a READ-ONLY pool.
	ro, err := analyticsstore.NewReadOnlyPool(ctx, analyticsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	reports := httptest.NewServer(inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{Reader: analyticsstore.NewReader(ro)}))
	defer reports.Close()

	from, to := day.Format(time.RFC3339), day.Add(24*time.Hour).Format(time.RFC3339)
	waitFor(t, 60*time.Second, "the projection to fold the analytics stream", func() bool {
		fresh := getJSON(t, reports.URL+"/reports/freshness")
		return fresh["as_of"] != nil
	})

	t.Run("the reports answer from the projected facts", func(t *testing.T) {
		mix := getJSON(t, reports.URL+"/reports/disposition-mix?from="+from+"&to="+to)
		rows, _ := mix["rows"].([]any)
		if len(rows) != 1 {
			t.Fatalf("mix rows = %v", mix["rows"])
		}
		row := rows[0].(map[string]any)
		if row["policy_version"] != float64(2) || row["outcome"] != "RESTOCK" || row["lines"] != float64(1) || row["units"] != float64(2) {
			t.Fatalf("mix row = %v", row)
		}

		recovery := getJSON(t, reports.URL+"/reports/value-recovery?from="+from+"&to="+to)
		days, _ := recovery["days"].([]any)
		if len(days) != 1 {
			t.Fatalf("recovery days = %v", recovery["days"])
		}
		sites := days[0].(map[string]any)["sites"].([]any)
		if len(sites) != 1 {
			t.Fatalf("recovery sites = %v", days[0])
		}
		site := sites[0].(map[string]any)
		if site["site_id"] != "SITE-AN" || site["restocked_lines"] != float64(1) || site["restocked_units"] != float64(2) {
			t.Fatalf("recovery site = %v", site)
		}

		cycle := getJSON(t, reports.URL+"/reports/return-cycle-time?from="+from+"&to="+to)
		days, _ = cycle["days"].([]any)
		if len(days) != 1 {
			t.Fatalf("cycle days = %v", cycle["days"])
		}
		lat := days[0].(map[string]any)["cycle_time"].(map[string]any)
		if lat["count"] != float64(1) || lat["p50_seconds"] != float64(390) {
			t.Fatalf("cycle_time = %v, want one 390s receipt-to-staged", lat)
		}
	})

	// The poison message was dead-lettered with its context, verbatim.
	dead := kafkatest.ReadN(t, analyticsTopic+inboundkafka.DLQSuffix, 1, 30*time.Second)
	if string(dead[0].Value) != string(tamperedValue) {
		t.Fatalf("dlq value differs from the poison message")
	}
	if headerOf(dead[0], "x-dlq-error") == "" {
		t.Fatalf("dlq message carries no x-dlq-error: %+v", dead[0].Headers)
	}

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return out
}

func headerOf(msg kafkatest.Message, key string) string {
	for k, v := range msg.Headers {
		if k == key {
			return v
		}
	}
	return ""
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}
