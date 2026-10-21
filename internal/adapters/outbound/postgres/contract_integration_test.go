//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
	"github.com/claudioed/returns-processing/internal/testing/repocontract"
)

// The same behavioural contract the in-memory doubles pass (see
// internal/adapters/outbound/memory/repocontract_test.go), run against a
// real Postgres: every contract case gets its own database.

func TestMain(m *testing.M) {
	m.Run()
	pgtest.Shutdown()
}

func TestRMARepoContract(t *testing.T) {
	repocontract.ReturnAuthorizationRepo(t, func(t *testing.T) ports.ReturnAuthorizationRepo {
		return postgres.NewRMAs(pgtest.NewPool(t))
	})
}

func TestReceiptRepoContract(t *testing.T) {
	repocontract.ReturnReceiptRepo(t, func(t *testing.T) ports.ReturnReceiptRepo {
		return postgres.NewReceipts(pgtest.NewPool(t))
	})
}

func TestDispositionRepoContract(t *testing.T) {
	repocontract.DispositionRepo(t, func(t *testing.T) ports.DispositionRepo {
		return postgres.NewDispositions(pgtest.NewPool(t))
	})
}

func TestPolicyRepoContract(t *testing.T) {
	repocontract.DispositionPolicyRepo(t, func(t *testing.T) ports.DispositionPolicyRepo {
		return postgres.NewPolicies(pgtest.NewPool(t))
	})
}

func TestIdempotencyStoreContract(t *testing.T) {
	repocontract.IdempotencyStore(t, func(t *testing.T) ports.IdempotencyStore {
		return postgres.NewIdempotencyStore(pgtest.NewPool(t))
	})
}

func TestProcessedEventsContract(t *testing.T) {
	repocontract.ProcessedEvents(t, func(t *testing.T) ports.ProcessedEvents {
		return postgres.NewProcessedEvents(pgtest.NewPool(t))
	})
}

func TestAllocatedLinesContract(t *testing.T) {
	repocontract.AllocatedLines(t, func(t *testing.T) repocontract.AllocatedLinesSeeds {
		return &pgAllocated{postgres.NewAllocatedLines(pgtest.NewPool(t))}
	})
}

func TestManifestedUnitsContract(t *testing.T) {
	repocontract.ManifestedUnits(t, func(t *testing.T) repocontract.ManifestedUnitsSeeds {
		return &pgManifested{postgres.NewManifestedUnits(pgtest.NewPool(t))}
	})
}

func TestProductClassesContract(t *testing.T) {
	repocontract.ProductClasses(t, func(t *testing.T) repocontract.ProductClassesSeeds {
		return &pgClasses{postgres.NewProductClasses(pgtest.NewPool(t))}
	})
}

// The seed adapters bridge the Apply* feeder methods to the contract's
// Seed* surface. A seeding failure fails the test loudly (panic).

type pgAllocated struct{ *postgres.AllocatedLines }

func (p *pgAllocated) SeedAllocated(orderID string, lineNo int32, sku string) {
	mustSeed(p.ApplyAllocatedLine(context.Background(), orderID, lineNo, sku))
}

type pgManifested struct{ *postgres.ManifestedUnits }

func (p *pgManifested) SeedManifested(orderID string, lineNo int32, packageID string) {
	mustSeed(p.ApplyManifestedUnit(context.Background(), orderID, lineNo, views.ManifestedUnit{
		PackageID: packageID, ManifestedAt: repocontract.T0,
	}))
}

type pgClasses struct{ *postgres.ProductClasses }

func (p *pgClasses) SeedClass(sku string, tags []string, temperatureClass string, version int64) {
	mustSeed(p.ApplyProductClass(context.Background(), sku, views.ProductClass{
		Tags: tags, TemperatureClass: temperatureClass, Version: version,
	}))
}

func mustSeed(err error) {
	if err != nil {
		panic(err)
	}
}
