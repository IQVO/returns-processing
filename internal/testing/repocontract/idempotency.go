package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// IdempotencyStore runs the IdempotencyStore contract over an EMPTY store:
// the same behavioural rules the HTTP middleware relies on, proven against
// the memory double and real Postgres alike.
func IdempotencyStore(t *testing.T, newStore func(t *testing.T) ports.IdempotencyStore) {
	t.Helper()
	t.Run("an unknown key is not found", func(t *testing.T) {
		if _, ok, err := newStore(t).Lookup(bg, "k"); err != nil || ok {
			t.Fatalf("Lookup = %v %v (err %v)", ok, err, err)
		}
	})
	t.Run("save then lookup round trips the answer", func(t *testing.T) {
		s := newStore(t)
		in := views.IdempotencyRecord{
			Key: "k1", Fingerprint: "fp", Status: 201, ContentType: "application/json",
			Body: []byte(`{"rmaId":"rma-0001"}`), CreatedAt: T0,
		}
		must(t, s.Save(bg, in))
		got, ok, err := s.Lookup(bg, "k1")
		must(t, err)
		if !ok || got.Fingerprint != "fp" || got.Status != 201 || got.ContentType != "application/json" ||
			string(got.Body) != string(in.Body) || !got.CreatedAt.Equal(T0) {
			t.Fatalf("got %+v (found %v)", got, ok)
		}
	})
	t.Run("the first writer of a key wins", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Save(bg, views.IdempotencyRecord{Key: "k", Fingerprint: "one", Status: 201, CreatedAt: T0}))
		must(t, s.Save(bg, views.IdempotencyRecord{Key: "k", Fingerprint: "two", Status: 409, CreatedAt: T0.Add(time.Hour)}))
		got, _, err := s.Lookup(bg, "k")
		must(t, err)
		if got.Fingerprint != "one" || got.Status != 201 {
			t.Fatalf("got %+v, want the first writer's answer", got)
		}
	})
	t.Run("an empty body stays empty, never NULL", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Save(bg, views.IdempotencyRecord{Key: "k", Fingerprint: "fp", Status: 204, CreatedAt: T0}))
		got, ok, err := s.Lookup(bg, "k")
		must(t, err)
		if !ok || len(got.Body) != 0 || got.Status != 204 {
			t.Fatalf("got %+v (found %v)", got, ok)
		}
	})
}
