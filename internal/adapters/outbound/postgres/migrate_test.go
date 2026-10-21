package postgres

import "testing"

// TestMigrationsURL proves the PgBouncer rule: the migrations run against
// MIGRATIONS_DATABASE_URL when it is set and non-empty, and fall back to
// DATABASE_URL otherwise.
func TestMigrationsURL(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"migrations url wins", map[string]string{
			EnvMigrationsDatabaseURL: "postgres://direct:5432/returns",
			EnvDatabaseURL:           "postgres://pooler:6432/returns",
		}, "postgres://direct:5432/returns"},
		{"fallback to database url", map[string]string{
			EnvDatabaseURL: "postgres://pooler:6432/returns",
		}, "postgres://pooler:6432/returns"},
		{"empty migrations url falls back", map[string]string{
			EnvMigrationsDatabaseURL: "",
			EnvDatabaseURL:           "postgres://pooler:6432/returns",
		}, "postgres://pooler:6432/returns"},
		{"neither set", map[string]string{}, ""},
	}
	for _, c := range cases {
		getenv := func(key string) string { return c.env[key] }
		if got := MigrationsURL(getenv); got != c.want {
			t.Fatalf("%s: MigrationsURL = %q, want %q", c.name, got, c.want)
		}
	}
}
