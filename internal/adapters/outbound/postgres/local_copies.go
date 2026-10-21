package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// AllocatedLines is the pgxpool-backed ports.AllocatedLines: the read side
// of the order-management local copy (ADR 0003). The port carries no ctx —
// a read is a single indexed lookup; the adapter bounds it with the pool's
// statement timeout.
type AllocatedLines struct {
	pool *pgxpool.Pool
}

// NewAllocatedLines constructs an AllocatedLines read model over pool.
func NewAllocatedLines(pool *pgxpool.Pool) *AllocatedLines { return &AllocatedLines{pool: pool} }

// Get implements ports.AllocatedLines (fail-closed: no row, no line — a
// line the copy does not know cannot be returned).
func (m *AllocatedLines) Get(orderID string, lineNo int32) (views.AllocatedLine, bool) {
	var line views.AllocatedLine
	err := queryFor(context.Background(), m.pool).QueryRow(context.Background(), `
		SELECT sku FROM allocated_lines WHERE order_id = $1 AND line_no = $2
	`, orderID, lineNo).Scan(&line.SKU)
	if err != nil {
		return views.AllocatedLine{}, false
	}
	return line, true
}

// ApplyAllocatedLine upserts one allocated order line (the consumer side of
// the copy; OrderAllocated / OrderPartiallyAllocated carry no version, so a
// newer fact is last-write-wins and the local version counter just bumps).
func (m *AllocatedLines) ApplyAllocatedLine(ctx context.Context, orderID string, lineNo int32, sku string) error {
	_, err := queryFor(ctx, m.pool).Exec(ctx, `
		INSERT INTO allocated_lines (order_id, line_no, sku, version)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (order_id, line_no) DO UPDATE
		SET sku = EXCLUDED.sku, version = allocated_lines.version + 1, updated_at = now()
	`, orderID, lineNo, sku)
	if err != nil {
		return fmt.Errorf("apply allocated line %s/%d: %w", orderID, lineNo, err)
	}
	return nil
}

// ManifestedUnits is the pgxpool-backed ports.ManifestedUnits: the read side
// of the fulfillment-execution package-manifest copy (ADR 0003).
type ManifestedUnits struct {
	pool *pgxpool.Pool
}

// NewManifestedUnits constructs a ManifestedUnits read model over pool.
func NewManifestedUnits(pool *pgxpool.Pool) *ManifestedUnits { return &ManifestedUnits{pool: pool} }

// GetByOrderLine implements ports.ManifestedUnits.
func (m *ManifestedUnits) GetByOrderLine(orderID string, lineNo int32) (views.ManifestedUnit, bool) {
	return m.manifested(`SELECT package_id, manifested_at FROM manifested_units WHERE work_unit = $1`, workUnitOf(orderID, lineNo))
}

// GetByPackage implements ports.ManifestedUnits.
func (m *ManifestedUnits) GetByPackage(packageID string) (views.ManifestedUnit, bool) {
	return m.manifested(`SELECT package_id, manifested_at FROM manifested_units WHERE package_id = $1`, packageID)
}

func (m *ManifestedUnits) manifested(sql string, key string) (views.ManifestedUnit, bool) {
	var (
		unit views.ManifestedUnit
		pkg  *string
		at   time.Time
	)
	if err := queryFor(context.Background(), m.pool).
		QueryRow(context.Background(), sql, key).Scan(&pkg, &at); err != nil {
		return views.ManifestedUnit{}, false
	}
	if pkg != nil {
		unit.PackageID = *pkg
	}
	unit.ManifestedAt = at.UTC()
	return unit, true
}

// ApplyManifestedUnit upserts one manifested work unit (the consumer side of
// the copy), keyed by the WES work-unit id `{order_id}-line-{line_no}` and
// indexed by its package id.
func (m *ManifestedUnits) ApplyManifestedUnit(ctx context.Context, orderID string, lineNo int32, unit views.ManifestedUnit) error {
	_, err := queryFor(ctx, m.pool).Exec(ctx, `
		INSERT INTO manifested_units (work_unit, order_id, line_no, package_id, manifested_at, version)
		VALUES ($1, $2, $3, $4, $5, 1)
		ON CONFLICT (work_unit) DO UPDATE
		SET package_id = EXCLUDED.package_id, manifested_at = EXCLUDED.manifested_at,
		    version = manifested_units.version + 1, updated_at = now()
	`, workUnitOf(orderID, lineNo), orderID, lineNo,
		nullString(unit.PackageID), unit.ManifestedAt.UTC())
	if err != nil {
		return fmt.Errorf("apply manifested unit %s/%d: %w", orderID, lineNo, err)
	}
	return nil
}

// workUnitOf is the WES work-unit formula, the same one
// shared.OrderLineRef.WorkUnit stamps.
func workUnitOf(orderID string, lineNo int32) string {
	return orderID + shared.WorkUnitSep + strconv.FormatInt(int64(lineNo), 10)
}

// ProductClasses is the pgxpool-backed ports.ProductClasses: the read side
// of the product-master classification copy (ADR 0003).
type ProductClasses struct {
	pool *pgxpool.Pool
}

// NewProductClasses constructs a ProductClasses read model over pool.
func NewProductClasses(pool *pgxpool.Pool) *ProductClasses { return &ProductClasses{pool: pool} }

// Get implements ports.ProductClasses (fail-open for grading: the "*"
// default rule catches an unclassified SKU).
func (m *ProductClasses) Get(sku string) (views.ProductClass, bool) {
	var (
		pc   views.ProductClass
		tags []byte
	)
	err := queryFor(context.Background(), m.pool).QueryRow(context.Background(), `
		SELECT tags, temperature_class, version FROM product_classes WHERE sku = $1
	`, sku).Scan(&tags, &pc.TemperatureClass, &pc.Version)
	if err != nil {
		return views.ProductClass{}, false
	}
	if err := json.Unmarshal(tags, &pc.Tags); err != nil {
		return views.ProductClass{}, false
	}
	return pc, true
}

// ApplyProductClass upserts one SKU's classification, applying only a NEWER
// version — the LOCAL COPY RULE of the contracts (ProductClassified carries
// a version; a replayed old version must not overwrite a newer copy).
func (m *ProductClasses) ApplyProductClass(ctx context.Context, sku string, pc views.ProductClass) error {
	tags, err := json.Marshal(pc.Tags)
	if err != nil {
		return fmt.Errorf("apply product class %s: %w", sku, err)
	}
	_, err = queryFor(ctx, m.pool).Exec(ctx, `
		INSERT INTO product_classes (sku, tags, temperature_class, version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (sku) DO UPDATE
		SET tags = EXCLUDED.tags, temperature_class = EXCLUDED.temperature_class,
		    version = EXCLUDED.version, updated_at = now()
		WHERE product_classes.version < EXCLUDED.version
	`, sku, tags, pc.TemperatureClass, pc.Version)
	if err != nil {
		return fmt.Errorf("apply product class %s: %w", sku, err)
	}
	return nil
}
