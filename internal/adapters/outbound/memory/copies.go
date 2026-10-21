package memory

import (
	"strconv"
	"sync"

	"github.com/claudioed/returns-processing/internal/application/views"
)

// This file holds the doubles of the ADR 0003 event-fed local-copy read
// ports: the maps a consumer step (or a test, or the BDD wiring) feeds and
// the use cases read. Put* is feeder-side; the Get methods are the port.

// AllocatedLines is the allocations copy double.
type AllocatedLines struct {
	mu    sync.Mutex
	lines map[string]views.AllocatedLine
}

// NewAllocatedLines returns an empty double.
func NewAllocatedLines() *AllocatedLines {
	return &AllocatedLines{lines: map[string]views.AllocatedLine{}}
}

// Put records the allocated order line.
func (a *AllocatedLines) Put(orderID string, lineNo int32, line views.AllocatedLine) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines[lineKey(orderID, lineNo)] = line
}

// Get returns the allocated line, or ok=false.
func (a *AllocatedLines) Get(orderID string, lineNo int32) (views.AllocatedLine, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, ok := a.lines[lineKey(orderID, lineNo)]
	return l, ok
}

// ManifestedUnits is the package-manifest copy double, keyed by the WES
// work unit `{order_id}-line-{line_no}` and by package id.
type ManifestedUnits struct {
	mu          sync.Mutex
	byOrderLine map[string]views.ManifestedUnit
	byPackage   map[string]views.ManifestedUnit
}

// NewManifestedUnits returns an empty double.
func NewManifestedUnits() *ManifestedUnits {
	return &ManifestedUnits{
		byOrderLine: map[string]views.ManifestedUnit{},
		byPackage:   map[string]views.ManifestedUnit{},
	}
}

// PutByOrderLine records the manifested work unit of the order line and
// indexes it by its package id.
func (m *ManifestedUnits) PutByOrderLine(orderID string, lineNo int32, unit views.ManifestedUnit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byOrderLine[lineKey(orderID, lineNo)] = unit
	if unit.PackageID != "" {
		m.byPackage[unit.PackageID] = unit
	}
}

// GetByOrderLine returns the line's manifested unit, or ok=false.
func (m *ManifestedUnits) GetByOrderLine(orderID string, lineNo int32) (views.ManifestedUnit, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byOrderLine[lineKey(orderID, lineNo)]
	return u, ok
}

// GetByPackage returns the package's manifested unit, or ok=false.
func (m *ManifestedUnits) GetByPackage(packageID string) (views.ManifestedUnit, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byPackage[packageID]
	return u, ok
}

// ProductClasses is the product-master classification copy double.
type ProductClasses struct {
	mu    sync.Mutex
	bySKU map[string]views.ProductClass
}

// NewProductClasses returns an empty double.
func NewProductClasses() *ProductClasses {
	return &ProductClasses{bySKU: map[string]views.ProductClass{}}
}

// Put records the SKU's classification copy.
func (p *ProductClasses) Put(sku string, pc views.ProductClass) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bySKU[sku] = pc
}

// Get returns the SKU's classification, or ok=false.
func (p *ProductClasses) Get(sku string) (views.ProductClass, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pc, ok := p.bySKU[sku]
	return pc, ok
}

func lineKey(orderID string, lineNo int32) string {
	return orderID + "\x00" + strconv.FormatInt(int64(lineNo), 10)
}
