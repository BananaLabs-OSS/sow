package sew

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tableExists reports whether a table is present (proves up/down actually ran).
func tableExists(t *testing.T, d *DB, name string) bool {
	t.Helper()
	var n int
	err := d.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestParseSections(t *testing.T) {
	m := Parse("001_create_widgets.sql", "-- +sew up\nCREATE TABLE w(id);\n-- +sew down\nDROP TABLE w;\n")
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
	writeFile(t, dir, "001_widgets.sql", "-- +sew up\nCREATE TABLE widgets(id INTEGER PRIMARY KEY);\n-- +sew down\nDROP TABLE widgets;\n")
	writeFile(t, dir, "002_gadgets.sql", "-- +sew up\nCREATE TABLE gadgets(id INTEGER);\nINSERT INTO gadgets VALUES (1);\n-- +sew down\nDROP TABLE gadgets;\n")

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
	var count int
	if err := db.sql.QueryRow(`SELECT count(*) FROM gadgets`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("multi-statement migration incomplete: count=%d err=%v", count, err)
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

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "001_x.sql", "-- +sew up\nCREATE TABLE x(id);\n")
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
	writeFile(t, dir, "001_noback.sql", "-- +sew up\nCREATE TABLE z(id);\n") // no down
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
