package sow

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// cellInt coerces a scanned count cell to int64 across backends.
func cellInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case []byte:
		n, _ := strconv.ParseInt(string(t), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

// queryCount runs a single-value count query over the DB's Conn.
func queryCount(t *testing.T, d *DB, q string, args ...any) int64 {
	t.Helper()
	rows, err := d.conn.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return 0
	}
	return cellInt(rows[0][0])
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tableExists reports whether a table is present (proves up/down actually ran).
func tableExists(t *testing.T, d *DB, name string) bool {
	t.Helper()
	return queryCount(t, d, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name) > 0
}

func TestParseSections(t *testing.T) {
	m := Parse("001_create_widgets.sql", "-- +sow up\nCREATE TABLE w(id);\n-- +sow down\nDROP TABLE w;\n")
	if m.Version != "001_create_widgets" {
		t.Errorf("version = %q", m.Version)
	}
	if m.Name != "create widgets" {
		t.Errorf("name = %q", m.Name)
	}
	if m.Up != "CREATE TABLE w(id);" || m.Down != "DROP TABLE w;" {
		t.Errorf("sections wrong: up=%q down=%q", m.Up, m.Down)
	}

	// No markers → all up, no down.
	m2 := Parse("002_x.sql", "CREATE TABLE y(id);")
	if m2.Up == "" || m2.Down != "" {
		t.Errorf("unmarked file: up=%q down=%q", m2.Up, m2.Down)
	}
}

func TestUpIdempotentAndDown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "001_widgets.sql", "-- +sow up\nCREATE TABLE widgets(id INTEGER PRIMARY KEY);\n-- +sow down\nDROP TABLE widgets;\n")
	writeFile(t, dir, "002_gadgets.sql", "-- +sow up\nCREATE TABLE gadgets(id INTEGER);\nINSERT INTO gadgets VALUES (1);\n-- +sow down\nDROP TABLE gadgets;\n")

	migs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 2 || migs[0].Version != "001_widgets" {
		t.Fatalf("load/order wrong: %+v", migs)
	}

	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	res, err := db.Up(migs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 2 {
		t.Fatalf("first up applied %d, want 2", len(res.Applied))
	}
	if !tableExists(t, db, "widgets") || !tableExists(t, db, "gadgets") {
		t.Fatal("tables not created")
	}
	// multi-statement migration ran fully (the INSERT too)
	if count := queryCount(t, db, `SELECT count(*) FROM gadgets`); count != 1 {
		t.Fatalf("multi-statement migration incomplete: count=%d", count)
	}

	// Idempotent: second up applies nothing.
	res2, _ := db.Up(migs, Options{})
	if len(res2.Applied) != 0 {
		t.Fatalf("second up applied %d, want 0", len(res2.Applied))
	}

	// Down one step rolls back the newest (gadgets), leaves widgets.
	rb, err := db.Down(migs, 1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.Rolled) != 1 || rb.Rolled[0].Version != "002_gadgets" {
		t.Fatalf("down rolled %+v", rb.Rolled)
	}
	if tableExists(t, db, "gadgets") {
		t.Error("gadgets should be gone after down")
	}
	if !tableExists(t, db, "widgets") {
		t.Error("widgets should remain")
	}

	// Re-up applies only the rolled-back one.
	res3, _ := db.Up(migs, Options{})
	if len(res3.Applied) != 1 || res3.Applied[0].Version != "002_gadgets" {
		t.Fatalf("re-up applied %+v", res3.Applied)
	}
}

// plainConn wraps a Conn but hides RunTx, so sow drives it best-effort — the path a
// Pulp host-backed connection (no cross-call transactions) takes.
type plainConn struct{ c Conn }

func (p plainConn) Exec(q string, a ...any) error            { return p.c.Exec(q, a...) }
func (p plainConn) Query(q string, a ...any) ([][]any, error) { return p.c.Query(q, a...) }

// TestInjectedConnBestEffort proves migrations run over an injected Conn that is NOT a
// TxConn — i.e. the driver-agnostic path a wasm cell uses.
func TestInjectedConnBestEffort(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "001_x.sql", "-- +sow up\nCREATE TABLE x(id INTEGER);\nINSERT INTO x VALUES (7);\n")
	migs, _ := Load(dir)

	native, err := Open(filepath.Join(dir, "inj.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()

	// Re-wrap the same underlying conn as a plain (non-Tx) Conn and run through New.
	injected, err := New(plainConn{c: native.conn})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := injected.conn.(TxConn); ok {
		t.Fatal("plainConn must not satisfy TxConn (that's the whole point)")
	}
	if _, err := injected.Up(migs, Options{}); err != nil {
		t.Fatal(err)
	}
	if !tableExists(t, injected, "x") || queryCount(t, injected, `SELECT count(*) FROM x`) != 1 {
		t.Fatal("best-effort injected Up did not apply the migration")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "001_x.sql", "-- +sow up\nCREATE TABLE x(id);\n")
	migs, _ := Load(dir)
	db, _ := Open(filepath.Join(dir, "d.db"))
	defer db.Close()

	if _, err := db.Up(migs, Options{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if tableExists(t, db, "x") {
		t.Error("dry-run created the table")
	}
	if vs, _ := db.AppliedVersions(); len(vs) != 0 {
		t.Error("dry-run recorded an applied version")
	}
}

func TestSplitStatementsRespectsQuotesAndComments(t *testing.T) {
	body := `INSERT INTO t VALUES ('a;b', "c;d"); -- trailing ; comment
	/* block ; comment */
	INSERT INTO t VALUES ('it''s ok');`
	got := splitStatements(body)
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2: %#v", len(got), got)
	}
}

func TestIrreversibleDownErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "001_noback.sql", "-- +sow up\nCREATE TABLE z(id);\n") // no down
	migs, _ := Load(dir)
	db, _ := Open(filepath.Join(dir, "n.db"))
	defer db.Close()
	if _, err := db.Up(migs, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Down(migs, 1, Options{}); err == nil {
		t.Error("expected error rolling back a migration with no down section")
	}
}
