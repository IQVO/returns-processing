//go:build integration

// The real-wiring proof of the shipped composition root: the test boots the
// REAL entry point (run, in-process) against testcontainers Postgres and
// Kafka with nothing but the environment a deployment sets, and drives it
// only through HTTP and Kafka. It reads effects from Postgres. The local
// copies are fed by the REAL consumers from the producers' exact events on
// the consumed topics — everything is the live path.
//
// The test broker has topic auto-creation OFF so the test controls when each
// topic appears (the cluster broker auto-creates topics on first write, the
// fleet convention, and readiness never depends on topics: subtest 1).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

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

// Topic names and types of this service's world (ADR 0004 + the consumed
// contracts of .claude/rules/integration-events.md).
const (
	topicEvents    = "warehouse.returns-processing.events"
	topicAnalytics = "warehouse.returns-processing.analytics"
	typeAuthorized = "com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorized"

	topicOrderMgmt = "warehouse.order-management.events"
	topicFulfill   = "warehouse.fulfillment.events"
	// The exact producer type strings the consumers dispatch on.
	typeOrderAllocated    = "com.warehouse.wes.order-management.order.OrderAllocated"
	typePackageManifested = "com.warehouse.wes.fulfillment-execution.package.PackageManifested"
)

// cloudEvent builds a valid CloudEvents 1.0 envelope of a CONSUMED type (the
// producer's source and type strings), and produce publishes the envelopes on
// topic — the shape order-management and fulfillment-execution really send.
func cloudEvent(t *testing.T, id, source, typ, subject string, data any) kafkatest.Message {
	t.Helper()
	e := ce.New(ce.CloudEventsVersionV1)
	e.SetID(id)
	e.SetSource(source)
	e.SetType(typ)
	e.SetSubject(subject)
	e.SetTime(time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC))
	e.SetDataSchema("urn:warehouse:consumed:" + typ + ":v1")
	if err := e.SetData("application/json", data); err != nil {
		t.Fatalf("set data: %v", err)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	value, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return kafkatest.Message{Key: subject, Value: value}
}

// freeAddr returns an address on an open port (listener closed at once).
func freeAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "127.0.0.1:0"
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// env is the environment a deployment sets.
type env struct {
	dbURL   string
	brokers string
	addr    string
}

func newEnv(t *testing.T, brokers string) env {
	t.Helper()
	return env{dbURL: pgtest.EmptyURL(t), brokers: brokers, addr: freeAddr()}
}

// apply sets the process environment of run (restored by the test cleanup).
func (e env) apply(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", e.dbURL)
	t.Setenv("KAFKA_BROKERS", e.brokers)
	t.Setenv("HTTP_ADDR", e.addr)
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "100ms")
	t.Setenv("LOG_LEVEL", "warn")
	for _, name := range consumerGroupEnvs {
		t.Setenv(name, "itest-"+name)
	}
}

// service is one running instance of the real entry point.
type service struct {
	t      *testing.T
	base   string
	cancel context.CancelFunc
	done   chan error
}

// start boots run in-process and waits until the listener answers /healthz.
func (e env) start(t *testing.T) *service {
	t.Helper()
	e.apply(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &service{t: t, base: "http://" + e.addr, cancel: cancel, done: make(chan error, 1)}
	go func() { s.done <- run(ctx) }()
	t.Cleanup(func() { s.stopQuietly() })
	waitFor(t, 60*time.Second, "the service to listen on "+e.addr, func() bool {
		select {
		case err := <-s.done:
			t.Fatalf("run returned while booting: %v", err)
		default:
		}
		status, _ := s.try(http.MethodGet, "/healthz", "")
		return status == http.StatusOK
	})
	return s
}

// stop cancels run and requires a clean shutdown within a bound.
func (s *service) stop() {
	s.t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(60 * time.Second):
		s.t.Fatal("run did not return after the shutdown")
	}
	s.done <- nil // let the cleanup find it stopped
}

func (s *service) stopQuietly() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(60 * time.Second):
	}
}

// try performs a request and returns -1 when the connection fails.
func (s *service) try(method, path, body string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		return -1, err.Error()
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// call asserts the status and returns the body.
func (s *service) call(method, path, body, key string, want int) string {
	s.t.Helper()
	status, raw := s.callRaw(method, path, body, key)
	if status != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, status, raw, want)
	}
	return raw
}

// callRaw sends the Idempotency-Key when non-empty.
func (s *service) callRaw(method, path, body, key string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		return -1, err.Error()
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// readyz returns the status and body of GET /readyz.
func (s *service) readyz() (int, string) {
	return s.try(http.MethodGet, "/readyz", "")
}

// topicExists answers whether the broker knows the topic (a partition with a
// leader). The service itself never calls a topic-admin API.
func topicExists(brokers []string, topic string) bool {
	conn, err := kafkago.Dial("tcp", brokers[0])
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	parts, err := conn.ReadPartitions(topic)
	return err == nil && len(parts) > 0 && parts[0].Leader.ID != 0
}

// The CloudEvents context attributes of a published message, plus its data.
type envelope struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            json.RawMessage `json:"data"`
}

// TestRealEntryPointRunsOnRealPostgresAndKafka drives the whole path: REST
// in, rows in Postgres, CloudEvents out through the outbox relay — plus the
// readiness contract on a topic-less broker.
func TestRealEntryPointRunsOnRealPostgresAndKafka(t *testing.T) {
	brokers := kafkatest.Brokers(t)
	e := newEnv(t, strings.Join(brokers, ","))
	pool := pgtest.Open(t, e.dbURL)
	s := e.start(t)

	t.Run("readyz is 200 while no topic exists", func(t *testing.T) {
		waitFor(t, 60*time.Second, "/readyz to be 200 although no topic exists", func() bool {
			status, _ := s.readyz()
			return status == http.StatusOK
		})
		if topicExists(brokers, topicEvents) {
			t.Fatalf("topic %s exists: the service must never create topics through an admin API", topicEvents)
		}
		if status, body := s.readyz(); status != http.StatusOK || !strings.Contains(body, "ready") {
			t.Fatalf("readyz with every topic missing = %d %s, want 200 ready", status, body)
		}
		// The test broker has auto-creation OFF: create the topic explicitly,
		// as the cluster broker would on the service's first write. The
		// analytics topic carries the twin of every published event
		// (ADR 0007); the relay writes both in one pass.
		kafkatest.CreateTopic(t, topicEvents)
		kafkatest.CreateTopic(t, topicAnalytics)
		if status, _ := s.readyz(); status != http.StatusOK {
			t.Fatalf("readyz after the topic appeared = %d, want 200", status)
		}
	})

	t.Run("writes require the idempotency key and replay it verbatim", func(t *testing.T) {
		const table = `{"returnWindowDays":30,"maxUnitsPerLine":5,"vetoOutcome":"DISPOSE","rules":[` +
			`{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"RESTOCK"},` +
			`{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}`
		status, raw := s.callRaw(http.MethodPut, "/disposition-policies/1", table, "")
		if status != http.StatusBadRequest || !strings.Contains(raw, "idempotency-key-required") {
			t.Fatalf("no key = %d %s", status, raw)
		}
		first := s.call(http.MethodPut, "/disposition-policies/1", table, "k-pol", http.StatusCreated)
		if replay := s.call(http.MethodPut, "/disposition-policies/1", table, "k-pol", http.StatusCreated); replay != first {
			t.Fatalf("replay differs:\n%s\n%s", first, replay)
		}
		other := strings.Replace(table, `"maxUnitsPerLine":5`, `"maxUnitsPerLine":6`, 1)
		status, raw = s.callRaw(http.MethodPut, "/disposition-policies/1", other, "k-pol")
		if status != http.StatusConflict || !strings.Contains(raw, "idempotency-key-reused") {
			t.Fatalf("reused key = %d %s", status, raw)
		}
	})

	t.Run("an authorize fails closed without the allocations copy and stores the rejection", func(t *testing.T) {
		const body = `{"orderId":"ord-e2e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`
		status, raw := s.callRaw(http.MethodPost, "/returns", body, "k-fail")
		if status != http.StatusConflict || !strings.Contains(raw, "ORDER_LINE_NOT_ALLOCATED") {
			t.Fatalf("unseeded authorize = %d %s", status, raw)
		}
		if n := pgtest.Count(t, pool, "return_authorizations"); n != 0 {
			t.Fatalf("a refused authorize persisted %d RMAs", n)
		}
		if n := pgtest.Count(t, pool, "outbox"); n != 0 {
			t.Fatalf("a refused authorize wrote %d outbox rows", n)
		}
		// The rejection itself replays.
		status2, raw2 := s.callRaw(http.MethodPost, "/returns", body, "k-fail")
		if status2 != status || raw2 != raw {
			t.Fatalf("rejection replay differs: %d %s", status2, raw2)
		}
	})

	// The consumers feed the local copies the authorize path reads: create
	// the consumed topics explicitly (the test broker has auto-creation OFF)
	// and publish the producers' exact events — the same path production
	// takes, REST in only for the authorize itself.
	kafkatest.CreateTopic(t, topicOrderMgmt)
	kafkatest.CreateTopic(t, topicFulfill)
	for _, line := range []struct{ order, sku string }{{"ord-e2e", "SKU-1"}, {"ord-race", "SKU-2"}} {
		kafkatest.Produce(t, topicOrderMgmt, cloudEvent(t,
			"alloc-"+line.order, "/warehouse/order-management", typeOrderAllocated, line.order,
			map[string]any{"order_id": line.order, "promise_date": "2026-11-21T10:01:00Z",
				"lines": []map[string]any{{"line_no": 1, "sku": line.sku, "path_id": "pick"}}}))
		kafkatest.Produce(t, topicFulfill, cloudEvent(t,
			"manifest-"+line.order, "/warehouse/fulfillment-execution", typePackageManifested, line.order+"-line-1",
			map[string]any{"package_id": "pkg-" + line.order, "order_ref": line.order + "-line-1"}))
	}
	waitFor(t, 90*time.Second, "the consumers to feed the local copies", func() bool {
		return pgtest.Count(t, pool, "allocated_lines") == 2 && pgtest.Count(t, pool, "manifested_units") == 2
	})
	if n := pgtest.Count(t, pool, "processed_events"); n != 4 {
		t.Fatalf("processed_events = %d, want 4 (one claim per consumed event)", n)
	}

	t.Run("authorize publishes the golden envelope through the relay", func(t *testing.T) {
		raw := s.call(http.MethodPost, "/returns",
			`{"orderId":"ord-e2e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`,
			"k-auth", http.StatusCreated)
		var created struct {
			RMAID string `json:"rmaId"`
		}
		if err := json.Unmarshal([]byte(raw), &created); err != nil || !strings.HasPrefix(created.RMAID, "rma-") {
			t.Fatalf("authorize = %s (%v)", raw, err)
		}
		// The relay drains the outbox row to the topic the broker now has.
		waitFor(t, 30*time.Second, "the outbox row to reach "+topicEvents, func() bool {
			return len(readEvents(t, 1)) == 1
		})
		msg := readEvents(t, 1)[0]
		var envlp envelope
		if err := json.Unmarshal(msg.Value, &envlp); err != nil {
			t.Fatalf("not a CloudEvent: %v: %s", err, msg.Value)
		}
		if envlp.SpecVersion != "1.0" || envlp.Type != typeAuthorized ||
			envlp.Source != "/warehouse/returns-processing" ||
			envlp.DataSchema != "urn:warehouse:returns-processing:events:ReturnAuthorized:v1" ||
			envlp.DataContentType != "application/json" || envlp.Subject != created.RMAID || envlp.ID == "" {
			t.Fatalf("envelope = %+v", envlp)
		}
		if msg.Key != created.RMAID {
			t.Fatalf("kafka key = %q, want the rma id %q", msg.Key, created.RMAID)
		}
		if got := msg.Headers["content-type"]; got != "application/cloudevents+json; charset=UTF-8" {
			t.Fatalf("content-type header = %q", got)
		}
		// The analytics twin of the same occurrence: the SAME CloudEvents id
		// and byte-identical data, only the dataschema stream segment differs
		// (ADR 0007).
		twin := kafkatest.ReadN(t, topicAnalytics, 1, 30*time.Second)[0]
		var tenv envelope
		if err := json.Unmarshal(twin.Value, &tenv); err != nil {
			t.Fatalf("the analytics twin is not a CloudEvent: %v: %s", err, twin.Value)
		}
		if tenv.ID != envlp.ID || tenv.Type != typeAuthorized || tenv.Subject != envlp.Subject ||
			string(tenv.Data) != string(envlp.Data) ||
			tenv.DataSchema != "urn:warehouse:returns-processing:analytics:ReturnAuthorized:v1" {
			t.Fatalf("analytics twin = %+v (integration %+v)", tenv, envlp)
		}
		// The read side sees the same RMA over HTTP.
		list := s.call(http.MethodGet, "/returns?orderRef=ord-e2e", "", "", http.StatusOK)
		if !strings.Contains(list, created.RMAID) {
			t.Fatalf("GET /returns?orderRef=ord-e2e = %s", list)
		}
		// The outbox row is deleted-on-send.
		waitFor(t, 30*time.Second, "the outbox to drain", func() bool {
			return pgtest.Count(t, pool, "outbox") == 0
		})
	})

	t.Run("concurrent requests with one key run once", func(t *testing.T) {
		const body = `{"orderId":"ord-race","lineNo":1,"sku":"SKU-2","quantity":1,"siteId":"WH1","reasonCode":"DAMAGED"}`
		const n = 8
		results := make([]string, n)
		var (
			wg       sync.WaitGroup
			statuses = make([]int, n)
			mu       sync.Mutex
		)
		first := ""
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status, raw := s.callRaw(http.MethodPost, "/returns", body, "k-race")
				mu.Lock()
				defer mu.Unlock()
				statuses[i] = status
				results[i] = raw
			}()
		}
		wg.Wait()
		mu.Lock()
		first = results[0]
		for i, raw := range results {
			if statuses[i] != http.StatusCreated || raw != first {
				mu.Unlock()
				t.Fatalf("result %d = %d %s, want the winner's 201 %s", i, statuses[i], raw, first)
			}
		}
		mu.Unlock()
		var rmas int
		pgtest.Scan(t, pool, `SELECT count(*) FROM return_authorizations WHERE order_id = $1`, []any{"ord-race"}, &rmas)
		if rmas != 1 {
			t.Fatalf("ord-race RMAs = %d, want 1", rmas)
		}
		var keys int
		pgtest.Scan(t, pool, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, []any{"k-race"}, &keys)
		if keys != 1 {
			t.Fatalf("k-race rows = %d, want 1", keys)
		}
	})

	s.stop()
}

// TestStoppingKafkaFlipsReadyzButNeverHealthz boots the service against a
// DEDICATED broker, waits for readiness, then stops the broker: /readyz flips
// to 503 dependency-unavailable naming kafka while /healthz stays 200.
func TestStoppingKafkaFlipsReadyzButNeverHealthz(t *testing.T) {
	ctx := context.Background()
	container, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1",
		tckafka.WithClusterID("returns-processing-readyz-itest"),
		testcontainers.WithEnv(map[string]string{"KAFKA_AUTO_CREATE_TOPICS_ENABLE": "false"}),
	)
	if err != nil {
		t.Fatalf("start dedicated kafka container: %v", err)
	}
	brokers, err := container.Brokers(ctx)
	if err != nil {
		t.Fatalf("dedicated broker address: %v", err)
	}
	e := newEnv(t, strings.Join(brokers, ","))
	s := e.start(t)
	waitFor(t, 60*time.Second, "/readyz to be 200 on the dedicated broker", func() bool {
		status, _ := s.readyz()
		return status == http.StatusOK
	})

	if err := testcontainers.TerminateContainer(container); err != nil {
		t.Fatalf("stop the dedicated kafka container: %v", err)
	}
	var last string
	deadline := time.Now().Add(60 * time.Second)
	down := false
	for time.Now().Before(deadline) {
		status, body := s.readyz()
		last = fmt.Sprintf("%d %s", status, body)
		if status == http.StatusServiceUnavailable && strings.Contains(body, "not ready: kafka") && !strings.Contains(body, "postgres") {
			down = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !down {
		t.Fatalf("readyz never reported kafka down; last = %s", last)
	}
	t.Logf("readyz after kafka stopped: %s", last)
	if status, _ := s.try(http.MethodGet, "/healthz", ""); status != http.StatusOK {
		t.Fatalf("healthz with kafka down = %d: liveness must not depend on Kafka", status)
	}
	s.stopQuietly()
}

// readEvents reads n messages from the events topic from the start.
func readEvents(t *testing.T, n int) []kafkatest.Message {
	t.Helper()
	return kafkatest.ReadN(t, topicEvents, n, 30*time.Second)
}
