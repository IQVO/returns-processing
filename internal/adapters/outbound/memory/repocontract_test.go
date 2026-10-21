package memory_test

import (
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/testing/repocontract"
)

// The same behavioural contract the Postgres adapters pass (see
// internal/adapters/outbound/postgres/contract_integration_test.go), run
// against the in-memory doubles in plain unit mode: what BDD and the unit
// tests exercise is what production does.

func TestRMARepoContract(t *testing.T) {
	repocontract.ReturnAuthorizationRepo(t, func(t *testing.T) ports.ReturnAuthorizationRepo {
		return memory.NewRMAs()
	})
}

func TestReceiptRepoContract(t *testing.T) {
	repocontract.ReturnReceiptRepo(t, func(t *testing.T) ports.ReturnReceiptRepo {
		return memory.NewReceipts()
	})
}

func TestDispositionRepoContract(t *testing.T) {
	repocontract.DispositionRepo(t, func(t *testing.T) ports.DispositionRepo {
		return memory.NewDispositions()
	})
}

func TestPolicyRepoContract(t *testing.T) {
	repocontract.DispositionPolicyRepo(t, func(t *testing.T) ports.DispositionPolicyRepo {
		return memory.NewPolicies()
	})
}

func TestAllocatedLinesContract(t *testing.T) {
	repocontract.AllocatedLines(t, func(t *testing.T) repocontract.AllocatedLinesSeeds {
		return &memoryAllocated{memory.NewAllocatedLines()}
	})
}

func TestManifestedUnitsContract(t *testing.T) {
	repocontract.ManifestedUnits(t, func(t *testing.T) repocontract.ManifestedUnitsSeeds {
		return &memoryManifested{memory.NewManifestedUnits()}
	})
}

func TestProductClassesContract(t *testing.T) {
	repocontract.ProductClasses(t, func(t *testing.T) repocontract.ProductClassesSeeds {
		return &memoryClasses{memory.NewProductClasses()}
	})
}

// The seed adapters bridge the doubles' feeder-side Put* methods to the
// contract's Seed* surface (the Postgres side bridges its Apply* methods the
// same way).

type memoryAllocated struct{ *memory.AllocatedLines }

func (m *memoryAllocated) SeedAllocated(orderID string, lineNo int32, sku string) {
	m.Put(orderID, lineNo, views.AllocatedLine{SKU: sku})
}

type memoryManifested struct{ *memory.ManifestedUnits }

func (m *memoryManifested) SeedManifested(orderID string, lineNo int32, packageID string) {
	m.PutByOrderLine(orderID, lineNo, views.ManifestedUnit{
		PackageID: packageID, ManifestedAt: repocontract.T0,
	})
}

type memoryClasses struct{ *memory.ProductClasses }

func (m *memoryClasses) SeedClass(sku string, tags []string, temperatureClass string, version int64) {
	m.Put(sku, views.ProductClass{Tags: tags, TemperatureClass: temperatureClass, Version: version})
}
