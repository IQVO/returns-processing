package usecases

import (
	"context"

	"github.com/claudioed/returns-processing/internal/application/ports"
)

// atomically runs fn inside uow when one is wired, unwrapped otherwise
// (memory wiring, tests). A nil UnitOfWork is tolerated by contract.
func atomically(ctx context.Context, uow ports.UnitOfWork, fn func(ctx context.Context) error) error {
	if uow == nil {
		return fn(ctx)
	}
	return uow.Do(ctx, fn)
}
