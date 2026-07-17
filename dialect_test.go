package sow

import "testing"

// The SQLite renderings below are byte-for-byte the literals sow shipped before dialects
// existed. If one of these fails, an existing SQLite caller's behaviour just changed.
func TestSQLiteDialectRendersLegacySQL(t *testing.T) {
	d := SQLite()
	cases := []struct{ got, want string }{
		{d.upsertLedgerSQL(), `INSERT OR IGNORE INTO sow_migrations (version, name, applied_at) VALUES (?, ?, ?)`},
		{d.insertLedgerSQL(), `INSERT INTO sow_migrations (version, name, applied_at) VALUES (?, ?, ?)`},
		{d.deleteLedgerSQL(), `DELETE FROM sow_migrations WHERE version = ?`},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("rendered\n  %s\nwant\n  %s", c.got, c.want)
		}
	}
}

func TestPostgresDialectRendersOnConflictAndDollarPlaceholders(t *testing.T) {
	d := Postgres()
	cases := []struct{ got, want string }{
		{d.upsertLedgerSQL(), `INSERT INTO sow_migrations (version, name, applied_at) VALUES ($1, $2, $3) ON CONFLICT (version) DO NOTHING`},
		{d.insertLedgerSQL(), `INSERT INTO sow_migrations (version, name, applied_at) VALUES ($1, $2, $3)`},
		{d.deleteLedgerSQL(), `DELETE FROM sow_migrations WHERE version = $1`},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("rendered\n  %s\nwant\n  %s", c.got, c.want)
		}
	}
}

// The zero Dialect must be SQLite, so New and any DB built without naming a dialect keep
// their pre-dialect behaviour.
func TestZeroDialectIsSQLite(t *testing.T) {
	var zero Dialect
	if got := zero.resolve().upsertLedgerSQL(); got != SQLite().upsertLedgerSQL() {
		t.Errorf("zero Dialect rendered %q, want the SQLite rendering", got)
	}
	if zero.String() != "sqlite" {
		t.Errorf("zero Dialect named %q, want sqlite", zero.String())
	}
}

// New must produce a SQLite DB — the backward-compatibility guarantee for existing callers.
func TestNewDefaultsToSQLiteDialect(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Dialect().String(); got != "sqlite" {
		t.Errorf("Open gave dialect %q, want sqlite", got)
	}
}

// A Postgres-dialect DB must render Postgres SQL over an injected Conn — the cell path,
// proven without a Postgres server by recording what sow tries to execute.
func TestNewWithDialectUsesPostgresSQL(t *testing.T) {
	rec := &recordConn{}
	db, err := NewWithDialect(rec, Postgres())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkApplied("001_x", "x", 5); err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO sow_migrations (version, name, applied_at) VALUES ($1, $2, $3) ON CONFLICT (version) DO NOTHING`
	if len(rec.execs) != 2 || rec.execs[1] != want {
		t.Errorf("MarkApplied issued %#v, want the ON CONFLICT rendering", rec.execs)
	}
}

// recordConn is a Conn that records statements instead of running them.
type recordConn struct{ execs []string }

func (r *recordConn) Exec(q string, _ ...any) error         { r.execs = append(r.execs, q); return nil }
func (r *recordConn) Query(string, ...any) ([][]any, error) { return nil, nil }
