package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Policies is the pgxpool-backed ports.DispositionPolicyRepo: one row per
// declared version (the version is the primary key), the latest one marked
// current by a partial unique index.
type Policies struct {
	pool *pgxpool.Pool
}

// NewPolicies constructs a Policies repo over pool.
func NewPolicies(pool *pgxpool.Pool) *Policies { return &Policies{pool: pool} }

const policyColumns = `version, return_window_days, max_units_per_line, veto_outcome, rules, declared_at`

// Save persists the policy version and marks it current, atomically: the
// previous current row is cleared first (unless it is this very version
// being re-declared), then the row is upserted with is_current set — a NEW
// version appends, a re-declared version replaces its row, exactly the
// memory double's semantics.
func (r *Policies) Save(ctx context.Context, p *disposition.DispositionPolicy) error {
	s := p.Snapshot()
	rules, err := marshalRules(s.Rules)
	if err != nil {
		return fmt.Errorf("save policy v%d: %w", s.Version, err)
	}
	return inTx(ctx, r.pool, func(q querier) error {
		if _, err := q.Exec(ctx, `
			UPDATE disposition_policies SET is_current = false
			WHERE is_current AND version <> $1
		`, s.Version); err != nil {
			return fmt.Errorf("clear current policy: %w", err)
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO disposition_policies (`+policyColumns+`, is_current)
			VALUES ($1, $2, $3, $4, $5, $6, true)
			ON CONFLICT (version) DO UPDATE
			SET return_window_days = EXCLUDED.return_window_days,
			    max_units_per_line = EXCLUDED.max_units_per_line,
			    veto_outcome = EXCLUDED.veto_outcome,
			    rules = EXCLUDED.rules,
			    declared_at = EXCLUDED.declared_at,
			    is_current = true
		`, s.Version, s.ReturnWindowDays, s.MaxUnitsPerLine, string(s.VetoOutcome),
			rules, s.DeclaredAt.UTC()); err != nil {
			return fmt.Errorf("save policy v%d: %w", s.Version, err)
		}
		return nil
	})
}

// marshalRules stores the rule table as a JSON array of
// {grade, productClass, outcome} objects.
func marshalRules(rules []disposition.Rule) ([]byte, error) {
	if len(rules) == 0 {
		return []byte("[]"), nil
	}
	out := make([]ruleJSON, 0, len(rules))
	for _, r := range rules {
		out = append(out, ruleJSON{
			Grade:        string(r.Grade),
			ProductClass: r.ProductClass,
			Outcome:      string(r.Outcome),
		})
	}
	return json.Marshal(out)
}

type ruleJSON struct {
	Grade        string `json:"grade"`
	ProductClass string `json:"productClass"`
	Outcome      string `json:"outcome"`
}

// Current implements ports.DispositionPolicyRepo: the row marked current.
func (r *Policies) Current(ctx context.Context) (*disposition.DispositionPolicy, error) {
	row := queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+policyColumns+` FROM disposition_policies WHERE is_current`)
	p, err := scanPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	return p, err
}

// FindByVersion implements ports.DispositionPolicyRepo.
func (r *Policies) FindByVersion(ctx context.Context, version int64) (*disposition.DispositionPolicy, error) {
	row := queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+policyColumns+` FROM disposition_policies WHERE version = $1`, version)
	p, err := scanPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	return p, err
}

func scanPolicy(row pgx.Row) (*disposition.DispositionPolicy, error) {
	var (
		s     disposition.PolicySnapshot
		veto  string
		rules []byte
	)
	if err := row.Scan(&s.Version, &s.ReturnWindowDays, &s.MaxUnitsPerLine,
		&veto, &rules, &s.DeclaredAt); err != nil {
		return nil, err
	}
	s.VetoOutcome = disposition.Outcome(veto)
	s.DeclaredAt = s.DeclaredAt.UTC()
	parsed, err := unmarshalRules(rules)
	if err != nil {
		return nil, fmt.Errorf("stored policy v%d is invalid: %w", s.Version, err)
	}
	s.Rules = parsed
	p, err := disposition.RestorePolicy(s)
	if err != nil {
		return nil, fmt.Errorf("stored policy v%d is invalid: %w", s.Version, err)
	}
	return p, nil
}

func unmarshalRules(b []byte) ([]disposition.Rule, error) {
	var in []ruleJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	rules := make([]disposition.Rule, 0, len(in))
	for _, r := range in {
		rules = append(rules, disposition.Rule{
			Grade:        shared.Grade(r.Grade),
			ProductClass: r.ProductClass,
			Outcome:      disposition.Outcome(r.Outcome),
		})
	}
	return rules, nil
}
