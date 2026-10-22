package analyticsstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/analytics/report"
)

// Reader is the Postgres READER (report.Reader). Days are UTC calendar days;
// every window is half-open [lo, hi). SQL counts, sums and takes percentiles
// (percentile_cont, the definition report.Percentile mirrors); the range
// rules, the site scope and the freshness lag live in
// internal/analytics/report.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader constructs a Reader over the (read-only) reports pool.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

var _ report.Reader = (*Reader)(nil)

// daysCTE builds the same windows as report.DayWindows: one per UTC day
// intersecting [$1, $2), clipped to the range. The day series runs over
// UTC-naive timestamps so a session time zone with DST cannot shift a day.
const daysCTE = `
	WITH days AS (
		SELECT d::date AS day,
		       GREATEST(d AT TIME ZONE 'UTC', $1::timestamptz)                    AS lo,
		       LEAST((d + interval '1 day') AT TIME ZONE 'UTC', $2::timestamptz) AS hi
		FROM generate_series(
		         date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC'),
		         ($2::timestamptz - interval '1 microsecond') AT TIME ZONE 'UTC',
		         interval '1 day') AS d
		WHERE $1::timestamptz < $2::timestamptz
	)`

// mixSQL groups the decisions of the range by policy version and outcome.
// The optional site scope joins the decision's receipt line: a decision
// whose line the projection has not seen (or whose line has no site yet) is
// out of a scoped query and in an unscoped one.
const mixSQL = `
	SELECT d.policy_version, d.outcome, count(*), COALESCE(sum(d.quantity), 0)::bigint
	FROM disposition_facts d
	LEFT JOIN return_line_facts l ON l.receipt_id = d.receipt_id AND l.line_no = d.line_no
	WHERE d.decided_at >= $1::timestamptz AND d.decided_at < $2::timestamptz
	  AND ($3 = '' OR l.site_id = $3)
	GROUP BY d.policy_version, d.outcome
	ORDER BY d.policy_version, d.outcome`

// DispositionMix implements report.Reader.
func (r *Reader) DispositionMix(ctx context.Context, q report.MixQuery) ([]report.MixRow, error) {
	rows, err := r.pool.Query(ctx, mixSQL, q.Range.From.UTC(), q.Range.To.UTC(), q.SiteID)
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: disposition mix: %w", err)
	}
	defer rows.Close()
	out := []report.MixRow{}
	for rows.Next() {
		var m report.MixRow
		if err := rows.Scan(&m.PolicyVersion, &m.Outcome, &m.Lines, &m.Units); err != nil {
			return nil, fmt.Errorf("analyticsstore: scan mix row: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analyticsstore: disposition mix rows: %w", err)
	}
	return out, nil
}

// recoverySQL cross joins the dense day series with every site that staged
// anything in the range's scope, so each day lists the same zero-filled
// sites in stable order.
const recoverySQL = daysCTE + `
	, sites AS (
		SELECT DISTINCT site_id
		FROM return_line_facts
		WHERE staged_at >= $1::timestamptz AND staged_at < $2::timestamptz
		  AND site_id IS NOT NULL AND ($3 = '' OR site_id = $3)
	)
	SELECT days.day, sites.site_id, staged.n, staged.units
	FROM days
	CROSS JOIN sites
	CROSS JOIN LATERAL (
		SELECT count(*) AS n, COALESCE(sum(r.quantity), 0)::bigint AS units
		FROM return_line_facts r
		WHERE r.site_id = sites.site_id AND r.staged_at >= days.lo AND r.staged_at < days.hi
	) staged
	ORDER BY days.day, sites.site_id`

// ValueRecoveryDays implements report.Reader: one dense row per DayWindow,
// each carrying one zero-filled entry per site of the range's scope.
func (r *Reader) ValueRecoveryDays(ctx context.Context, q report.SiteQuery) ([]report.RecoveryDay, error) {
	rows, err := r.pool.Query(ctx, recoverySQL, q.Range.From.UTC(), q.Range.To.UTC(), q.SiteID)
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: value recovery days: %w", err)
	}
	defer rows.Close()
	out := []report.RecoveryDay{}
	for rows.Next() {
		var day time.Time
		var s report.RecoverySite
		if err := rows.Scan(&day, &s.SiteID, &s.RestockedLines, &s.RestockedUnits); err != nil {
			return nil, fmt.Errorf("analyticsstore: scan recovery row: %w", err)
		}
		if len(out) == 0 || !out[len(out)-1].Day.Equal(dayUTC(day)) {
			out = append(out, report.RecoveryDay{Day: dayUTC(day)})
		}
		out[len(out)-1].Sites = append(out[len(out)-1].Sites, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analyticsstore: value recovery rows: %w", err)
	}
	return out, nil
}

// cycleSQL counts the lines staged in each window and takes the
// opened-to-staged percentiles of those whose receipt opening was seen. A
// negative duration (events recorded out of order) counts as zero.
const cycleSQL = daysCTE + `
	SELECT days.day, staged.n, latency.n, latency.p50, latency.p95
	FROM days
	CROSS JOIN LATERAL (
		SELECT count(*) AS n
		FROM return_line_facts r
		WHERE ($3 = '' OR r.site_id = $3) AND r.staged_at >= days.lo AND r.staged_at < days.hi
	) staged
	CROSS JOIN LATERAL (
		SELECT count(*) AS n,
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY s.secs) AS p50,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY s.secs) AS p95
		FROM (SELECT GREATEST(0, EXTRACT(EPOCH FROM r.staged_at - r.opened_at))::float8 AS secs
		      FROM return_line_facts r
		      WHERE ($3 = '' OR r.site_id = $3) AND r.opened_at IS NOT NULL
		        AND r.staged_at >= days.lo AND r.staged_at < days.hi) s
	) latency
	ORDER BY days.day`

// CycleTimeDays implements report.Reader: one dense row per DayWindow.
func (r *Reader) CycleTimeDays(ctx context.Context, q report.SiteQuery) ([]report.CycleDay, error) {
	rows, err := r.pool.Query(ctx, cycleSQL, q.Range.From.UTC(), q.Range.To.UTC(), q.SiteID)
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: cycle time days: %w", err)
	}
	defer rows.Close()
	out := []report.CycleDay{}
	for rows.Next() {
		var d report.CycleDay
		var day time.Time
		if err := rows.Scan(&day, &d.StagedLines, &d.CycleTime.Count, &d.CycleTime.P50, &d.CycleTime.P95); err != nil {
			return nil, fmt.Errorf("analyticsstore: scan cycle day: %w", err)
		}
		d.Day = dayUTC(day)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analyticsstore: cycle time rows: %w", err)
	}
	return out, nil
}

// LastEventAt implements report.Reader: the newest CloudEvents time among the
// applied events, nil while none was applied.
func (r *Reader) LastEventAt(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	if err := r.pool.QueryRow(ctx, `SELECT max(occurred_at) FROM analytics_processed_events`).Scan(&at); err != nil {
		return nil, fmt.Errorf("analyticsstore: last event: %w", err)
	}
	if at != nil {
		utc := at.UTC()
		at = &utc
	}
	return at, nil
}

// dayUTC normalises a scanned date to its midnight UTC, the Day of a report
// row.
func dayUTC(d time.Time) time.Time {
	y, m, dd := d.Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
}
