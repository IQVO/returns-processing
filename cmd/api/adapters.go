package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// adapters is the set of outbound adapters the composition root wires. They
// are the real ones — Postgres for every repository, the outbox, the unit of
// work, the idempotency store — there is no in-memory variant in the binary
// (internal/architecture's TestRuntimeBinaryHasNoInMemoryAdapters).
type adapters struct {
	rmas       ports.ReturnAuthorizationRepo
	receipts   ports.ReturnReceiptRepo
	dispos     ports.DispositionRepo
	policies   ports.DispositionPolicyRepo
	allocated  ports.AllocatedLines
	manifested ports.ManifestedUnits
	classes    ports.ProductClasses
	events     ports.EventPublisher
	uow        ports.UnitOfWork
	clock      ports.Clock
	ids        ports.IDGen
	// idempotency is the Idempotency-Key middleware of the writes.
	idempotency func(http.Handler) http.Handler
	// ping is the Postgres liveness check behind /readyz.
	ping func(context.Context) error
}

// buildAdapters wires the Postgres adapters over pool: every repository, the
// transactional outbox, the unit of work, the idempotency store, the wall
// clock and the uuid id generator.
func buildAdapters(pool *pgxpool.Pool) adapters {
	ad := adapters{
		rmas:       postgres.NewRMAs(pool),
		receipts:   postgres.NewReceipts(pool),
		dispos:     postgres.NewDispositions(pool),
		policies:   postgres.NewPolicies(pool),
		allocated:  postgres.NewAllocatedLines(pool),
		manifested: postgres.NewManifestedUnits(pool),
		classes:    postgres.NewProductClasses(pool),
		events:     postgres.NewOutbox(pool),
		uow:        postgres.NewUnitOfWork(pool),
		clock:      postgres.NewClock(),
		ids:        postgres.NewIDGen(),
		ping:       pool.Ping,
	}
	ad.idempotency = inboundhttp.NewIdempotency(postgres.NewIdempotencyStore(pool), ad.uow, ad.clock).Middleware
	return ad
}

// buildServer wires the use cases into the HTTP adapter.
func buildServer(a adapters, readiness *inboundhttp.Readiness) *inboundhttp.Server {
	return &inboundhttp.Server{
		AuthorizeReturn: &usecases.AuthorizeReturn{
			RMAs: a.rmas, Policies: a.policies, Allocated: a.allocated, Manifested: a.manifested,
			Events: a.events, IDs: a.ids, Clock: a.clock, UnitOfWork: a.uow,
		},
		GetReturn:   &usecases.GetReturn{RMAs: a.rmas},
		ListByOrder: &usecases.ListReturnsByOrder{RMAs: a.rmas},
		OpenReturnReceipt: &usecases.OpenReturnReceipt{
			Receipts: a.receipts, Events: a.events, IDs: a.ids, Clock: a.clock, UnitOfWork: a.uow,
		},
		InspectReturnLine: &usecases.InspectReturnLine{
			Receipts: a.receipts, Dispositions: a.dispos, Policies: a.policies, Products: a.classes,
			Events: a.events, IDs: a.ids, Clock: a.clock, UnitOfWork: a.uow,
		},
		OverrideDisposition: &usecases.OverrideDisposition{
			Dispositions: a.dispos, Events: a.events, IDs: a.ids, Clock: a.clock, UnitOfWork: a.uow,
		},
		DeclareDispositionPolicy: &usecases.DeclareDispositionPolicy{Policies: a.policies, Clock: a.clock, UnitOfWork: a.uow},
		GetDispositionPolicy:     &usecases.GetDispositionPolicy{Policies: a.policies},
		Idempotency:              a.idempotency,
		Readiness:                readiness,
	}
}

// buildReadiness makes /readyz real: the drain gate AND a Postgres ping AND
// a Kafka metadata check (a broker answers a topic-less metadata request;
// reachability only, never topics: the broker auto-creates them on first
// write). The verdict is cached for inboundhttp.DefaultReadinessTTL.
func buildReadiness(a adapters, kafkaCheck func(context.Context) error) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(0, 0,
		inboundhttp.Dependency{Name: "postgres", Check: a.ping},
		inboundhttp.Dependency{Name: "kafka", Check: kafkaCheck},
	)
}
