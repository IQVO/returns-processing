package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/returns-processing/internal/analytics/report"
)

// ReportsServer is the inbound HTTP adapter of cmd/returns-reports: the
// read-only reports of the analytics read side (ADR 0007). It depends only
// on the report.Reader port over the analytical database; it never touches
// the OLTP use cases, and it writes nothing.
type ReportsServer struct {
	Reader report.Reader
	// Now is the clock the default range ends at and the freshness lag is
	// measured against (time.Now when nil).
	Now func() time.Time
}

const dayLayout = "2006-01-02"

// The wire shapes (snake_case like the other fleet reports); days are UTC
// calendar dates and the report structs never leak onto the wire.
type timingDTO struct {
	Count      int      `json:"count"`
	P50Seconds *float64 `json:"p50_seconds"`
	P95Seconds *float64 `json:"p95_seconds"`
}

type mixRowDTO struct {
	PolicyVersion int64  `json:"policy_version"`
	Outcome       string `json:"outcome"`
	Lines         int    `json:"lines"`
	Units         int64  `json:"units"`
}

type mixReportDTO struct {
	From   time.Time   `json:"from"`
	To     time.Time   `json:"to"`
	SiteID string      `json:"site_id,omitempty"`
	Rows   []mixRowDTO `json:"rows"`
}

type recoverySiteDTO struct {
	SiteID         string `json:"site_id"`
	RestockedLines int    `json:"restocked_lines"`
	RestockedUnits int64  `json:"restocked_units"`
}

type recoveryDayDTO struct {
	Day   time.Time         `json:"day"`
	Sites []recoverySiteDTO `json:"sites"`
}

type recoveryReportDTO struct {
	From        time.Time        `json:"from"`
	To          time.Time        `json:"to"`
	SiteID      string           `json:"site_id,omitempty"`
	Granularity string           `json:"granularity"`
	Days        []recoveryDayDTO `json:"days"`
}

type cycleDayDTO struct {
	Day         string    `json:"day"`
	StagedLines int       `json:"staged_lines"`
	CycleTime   timingDTO `json:"cycle_time"`
}

type cycleReportDTO struct {
	From        time.Time     `json:"from"`
	To          time.Time     `json:"to"`
	SiteID      string        `json:"site_id,omitempty"`
	Granularity string        `json:"granularity"`
	Days        []cycleDayDTO `json:"days"`
}

// Problems of the reports binary.
var (
	invalidReportRange = problem{http.StatusBadRequest, "invalid-report-range",
		"from and to must form a valid RFC 3339 range of at most 366 days", ""}
	invalidReportGranularity = problem{http.StatusBadRequest, "invalid-report-granularity",
		"granularity must be \"day\"", ""}
	reportStoreError = problem{http.StatusInternalServerError, "report-store-error",
		"The report could not be served", "the analytical database query failed"}
)

// NewReportsRouter builds the router of cmd/returns-reports: /healthz,
// GET /reports/disposition-mix, /reports/value-recovery,
// /reports/return-cycle-time and /reports/freshness. No auth middleware
// (fleet rule), no CORS: the service is cluster-internal.
func NewReportsRouter(s *ReportsServer) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverPanic)
	r.Get("/healthz", handleHealthz)
	r.Get("/reports/disposition-mix", s.handleDispositionMix)
	r.Get("/reports/value-recovery", s.handleValueRecovery)
	r.Get("/reports/return-cycle-time", s.handleReturnCycleTime)
	r.Get("/reports/freshness", s.handleFreshness)
	return r
}

func (s *ReportsServer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func timingToDTO(t report.Timing) timingDTO {
	return timingDTO{Count: t.Count, P50Seconds: t.P50, P95Seconds: t.P95}
}

// parseReportInputs parses the shared from/to/siteId/granularity query
// values every report accepts (the mix report serves whole-range aggregates
// and ignores the day bucketing; day is the only granularity today).
func (s *ReportsServer) parseReportInputs(w http.ResponseWriter, r *http.Request) (report.Range, string, report.Granularity, bool) {
	q := r.URL.Query()
	rg, err := report.ParseRange(q.Get("from"), q.Get("to"), s.now())
	if err != nil {
		writeProblem(w, r, invalidReportRange, err.Error())
		return report.Range{}, "", "", false
	}
	granularity, err := report.ParseGranularity(q.Get("granularity"))
	if err != nil {
		writeProblem(w, r, invalidReportGranularity, err.Error())
		return report.Range{}, "", "", false
	}
	return rg, q.Get("siteId"), granularity, true
}

// handleDispositionMix serves the outcome counts by policy version over
// ?from=&to=&siteId= (a decision counts in the day its DispositionDecided
// `time` falls in).
func (s *ReportsServer) handleDispositionMix(w http.ResponseWriter, r *http.Request) {
	rg, siteID, _, ok := s.parseReportInputs(w, r)
	if !ok {
		return
	}
	rows, err := s.Reader.DispositionMix(r.Context(), report.MixQuery{Range: rg, SiteID: siteID})
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	out := mixReportDTO{From: rg.From, To: rg.To, SiteID: siteID, Rows: make([]mixRowDTO, 0, len(rows))}
	for _, row := range rows {
		out.Rows = append(out.Rows, mixRowDTO{PolicyVersion: row.PolicyVersion, Outcome: row.Outcome, Lines: row.Lines, Units: row.Units})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleValueRecovery serves the restocked lines and units per site per day
// over ?from=&to=&siteId=&granularity=day.
func (s *ReportsServer) handleValueRecovery(w http.ResponseWriter, r *http.Request) {
	rg, siteID, granularity, ok := s.parseReportInputs(w, r)
	if !ok {
		return
	}
	days, err := s.Reader.ValueRecoveryDays(r.Context(), report.SiteQuery{Range: rg, SiteID: siteID, Granularity: granularity})
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	out := recoveryReportDTO{
		From: rg.From, To: rg.To, SiteID: siteID,
		Granularity: string(granularity), Days: make([]recoveryDayDTO, 0, len(days)),
	}
	for _, d := range days {
		day := recoveryDayDTO{Day: d.Day, Sites: make([]recoverySiteDTO, 0, len(d.Sites))}
		for _, site := range d.Sites {
			day.Sites = append(day.Sites, recoverySiteDTO{SiteID: site.SiteID, RestockedLines: site.RestockedLines, RestockedUnits: site.RestockedUnits})
		}
		out.Days = append(out.Days, day)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReturnCycleTime serves the receipt-to-staged latency percentiles per
// day over ?from=&to=&siteId=&granularity=day.
func (s *ReportsServer) handleReturnCycleTime(w http.ResponseWriter, r *http.Request) {
	rg, siteID, granularity, ok := s.parseReportInputs(w, r)
	if !ok {
		return
	}
	days, err := s.Reader.CycleTimeDays(r.Context(), report.SiteQuery{Range: rg, SiteID: siteID, Granularity: granularity})
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	out := cycleReportDTO{
		From: rg.From, To: rg.To, SiteID: siteID,
		Granularity: string(granularity), Days: make([]cycleDayDTO, 0, len(days)),
	}
	for _, d := range days {
		out.Days = append(out.Days, cycleDayDTO{Day: d.Day.UTC().Format(dayLayout), StagedLines: d.StagedLines, CycleTime: timingToDTO(d.CycleTime)})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFreshness serves how far the projection is behind (now - the newest
// applied event time). Both fields are null until the first event is applied.
func (s *ReportsServer) handleFreshness(w http.ResponseWriter, r *http.Request) {
	asOf, err := s.Reader.LastEventAt(r.Context())
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report.ComputeFreshness(asOf, s.now()))
}

// writeReportStoreError is the 500 for a failed analytical query. The cause is
// logged, never echoed: it can carry SQL and connection details.
func writeReportStoreError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "report query failed", "error", err, "path", logSafe(r.URL.Path))
	writeProblem(w, r, reportStoreError, reportStoreError.detail)
}
