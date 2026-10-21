package repocontract

import (
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
)

// AllocatedLinesSeeds feeds one allocated order line into the store (the
// consumer side of the copy): memory's Put, Postgres's ApplyAllocatedLine.
type AllocatedLinesSeeds interface {
	ports.AllocatedLines
	SeedAllocated(orderID string, lineNo int32, sku string)
}

// AllocatedLines runs the allocations-copy contract on both adapters.
func AllocatedLines(t *testing.T, newStore func(t *testing.T) AllocatedLinesSeeds) {
	t.Helper()
	run(t, newStore, map[string]func(t *testing.T, s AllocatedLinesSeeds){
		"an unknown order line is not allocated": allocatedMiss,
		"a seeded line reads back its sku":       allocatedHit,
		"a newer fact overwrites the sku":        allocatedOverwrite,
	})
}

func allocatedMiss(t *testing.T, s AllocatedLinesSeeds) {
	if _, ok := s.Get("ord-1", 2); ok {
		t.Fatal("an unseeded line must read as not allocated")
	}
}

func allocatedHit(t *testing.T, s AllocatedLinesSeeds) {
	s.SeedAllocated("ord-1", 2, "sku-1")
	line, ok := s.Get("ord-1", 2)
	if !ok || line.SKU != "sku-1" {
		t.Fatalf("get = %+v ok=%t", line, ok)
	}
	if _, ok := s.Get("ord-1", 3); ok {
		t.Fatal("another line of the order must not read as allocated")
	}
}

func allocatedOverwrite(t *testing.T, s AllocatedLinesSeeds) {
	s.SeedAllocated("ord-1", 2, "sku-1")
	s.SeedAllocated("ord-1", 2, "sku-9")
	line, ok := s.Get("ord-1", 2)
	if !ok || line.SKU != "sku-9" {
		t.Fatalf("get = %+v ok=%t", line, ok)
	}
}

// ManifestedUnitsSeeds feeds one manifested work unit into the store.
type ManifestedUnitsSeeds interface {
	ports.ManifestedUnits
	SeedManifested(orderID string, lineNo int32, packageID string)
}

// ManifestedUnits runs the package-manifest-copy contract on both adapters.
func ManifestedUnits(t *testing.T, newStore func(t *testing.T) ManifestedUnitsSeeds) {
	t.Helper()
	run(t, newStore, map[string]func(t *testing.T, s ManifestedUnitsSeeds){
		"an unmanifested line reads as missing":    manifestedMiss,
		"a seeded unit reads by line and package":  manifestedHit,
		"the work-unit key is order and line":      manifestedKeying,
		"the package lookup misses unknown ids":    manifestedPackageMiss,
		"a re-manifested line overwrites the unit": manifestedOverwrite,
	})
}

func manifestedMiss(t *testing.T, s ManifestedUnitsSeeds) {
	if _, ok := s.GetByOrderLine("ord-1", 2); ok {
		t.Fatal("an unseeded line must read as not manifested")
	}
}

func manifestedHit(t *testing.T, s ManifestedUnitsSeeds) {
	s.SeedManifested("ord-1", 2, "pkg-1")
	byLine, ok := s.GetByOrderLine("ord-1", 2)
	if !ok || byLine.PackageID != "pkg-1" {
		t.Fatalf("by line = %+v ok=%t", byLine, ok)
	}
	if byLine.ManifestedAt.IsZero() {
		t.Fatal("manifested at lost")
	}
	byPkg, ok := s.GetByPackage("pkg-1")
	if !ok || byPkg.PackageID != "pkg-1" {
		t.Fatalf("by package = %+v ok=%t", byPkg, ok)
	}
}

func manifestedKeying(t *testing.T, s ManifestedUnitsSeeds) {
	s.SeedManifested("ord-1", 2, "pkg-1")
	if _, ok := s.GetByOrderLine("ord-1", 3); ok {
		t.Fatal("another line of the order must not read as manifested")
	}
	if _, ok := s.GetByOrderLine("ord-2", 2); ok {
		t.Fatal("the same line of another order must not read as manifested")
	}
}

func manifestedPackageMiss(t *testing.T, s ManifestedUnitsSeeds) {
	if _, ok := s.GetByPackage("nope"); ok {
		t.Fatal("an unknown package must read as missing")
	}
}

func manifestedOverwrite(t *testing.T, s ManifestedUnitsSeeds) {
	s.SeedManifested("ord-1", 2, "pkg-1")
	s.SeedManifested("ord-1", 2, "pkg-2")
	byLine, ok := s.GetByOrderLine("ord-1", 2)
	if !ok || byLine.PackageID != "pkg-2" {
		t.Fatalf("by line = %+v ok=%t", byLine, ok)
	}
	if _, ok := s.GetByPackage("pkg-1"); ok {
		t.Fatal("the replaced package must not be found")
	}
}

// ProductClassesSeeds feeds one classification into the store. The version
// rules the seeds: only a strictly newer version overwrites (the LOCAL COPY
// RULE — ProductClassified carries a version).
type ProductClassesSeeds interface {
	ports.ProductClasses
	SeedClass(sku string, tags []string, temperatureClass string, version int64)
}

// ProductClasses runs the classification-copy contract on both adapters.
func ProductClasses(t *testing.T, newStore func(t *testing.T) ProductClassesSeeds) {
	t.Helper()
	run(t, newStore, map[string]func(t *testing.T, s ProductClassesSeeds){
		"an unclassified sku reads as missing": classMiss,
		"a seeded classification reads back":   classHit,
		"a newer version overwrites":           classNewerWins,
		"an older or equal version does not":   classOlderLoses,
	})
}

func classMiss(t *testing.T, s ProductClassesSeeds) {
	if _, ok := s.Get("sku-1"); ok {
		t.Fatal("an unseeded sku must read as unclassified")
	}
}

func classHit(t *testing.T, s ProductClassesSeeds) {
	s.SeedClass("sku-1", []string{"class:Standard"}, "Ambient", 3)
	pc, ok := s.Get("sku-1")
	if !ok || pc.TemperatureClass != "Ambient" || pc.Version != 3 {
		t.Fatalf("get = %+v ok=%t", pc, ok)
	}
	if len(pc.Tags) != 1 || pc.Tags[0] != "class:Standard" {
		t.Fatalf("tags = %v", pc.Tags)
	}
}

func classNewerWins(t *testing.T, s ProductClassesSeeds) {
	s.SeedClass("sku-1", []string{"class:Standard"}, "Ambient", 3)
	s.SeedClass("sku-1", []string{"class:Hazmat", "hazmat", "dot:3"}, "Frozen", 4)
	pc, ok := s.Get("sku-1")
	if !ok || pc.Version != 4 || pc.TemperatureClass != "Frozen" {
		t.Fatalf("get = %+v ok=%t", pc, ok)
	}
}

func classOlderLoses(t *testing.T, s ProductClassesSeeds) {
	s.SeedClass("sku-1", []string{"class:Hazmat", "hazmat", "dot:3"}, "Frozen", 4)
	// a replayed older version must not overwrite the newer copy
	s.SeedClass("sku-1", []string{"class:Standard"}, "Ambient", 3)
	pc, ok := s.Get("sku-1")
	if !ok || pc.Version != 4 || pc.TemperatureClass != "Frozen" {
		t.Fatalf("get = %+v ok=%t (an old version overwrote the copy)", pc, ok)
	}
}
