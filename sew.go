// Package sew is a small, general-purpose SQLite migration runner: point it at any
// SQLite database and a directory of `.sql` migration files, and it applies the
// pending ones in order, once, inside a transaction, recording what it applied so
// re-running is a no-op. Rollbacks run each migration's `down` section. No ORM, no
// codegen, no framework — read migrations, apply them, sew it up.
//
// A migration file is plain SQL with two sections:
//
//	-- +sew up
//	CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT);
//
//	-- +sew down
//	DROP TABLE widgets;
//
// The file's name (without .sql) is its VERSION and its sort key, so a zero-padded
// or timestamped prefix keeps order deterministic: 001_init.sql, 20260708_add_col.sql.
// A file with no markers is treated as all-up (no rollback).
package sew

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite" (no CGo)
)

// Migration is one versioned step: its Up SQL, optional Down SQL, and identity.
type Migration struct {
	Version string // sort key / unique id (the file name without .sql)
	Name    string // human label (the part after the leading version token)
	Up      string // SQL applied when migrating forward
	Down    string // SQL applied when rolling back ("" = irreversible)
	Path    string // source file, for error messages ("" for in-memory)
}

// ledgerDDL records which versions have been applied. Created on first use, so a
// fresh database needs no bootstrap step.
const ledgerDDL = `CREATE TABLE IF NOT EXISTS sew_migrations (
	version    TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	applied_at INTEGER NOT NULL DEFAULT 0
)`

// Options tune an Up/Down run.
type Options struct {
	DryRun bool                          // plan only — open no transaction, write nothing
	Logf   func(format string, a ...any) // progress sink (nil = silent)
	Now    func() int64                  // applied_at clock (nil = 0); injectable for tests
}

func (o Options) log(format string, a ...any) {
	if o.Logf != nil {
		o.Logf(format, a...)
	}
}

func (o Options) now() int64 {
	if o.Now != nil {
		return o.Now()
	}
	return 0
}

// Result summarizes what a run did.
type Result struct {
	Applied []Migration // migrations whose Up ran (Up direction)
	Rolled  []Migration // migrations whose Down ran (Down direction)
}

// DB is an open SQLite database with the sew ledger ensured.
type DB struct{ sql *sql.DB }

// Open opens (or creates) a SQLite database at path (":memory:" allowed) and ensures
// the ledger table exists.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sew: open %q: %w", path, err)
	}
	if _, err := db.Exec(ledgerDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sew: init ledger: %w", err)
	}
	return &DB{sql: db}, nil
}

// Close releases the database.
func (d *DB) Close() error { return d.sql.Close() }

// AppliedVersions returns the applied versions in application order.
func (d *DB) AppliedVersions() ([]string, error) {
	rows, err := d.sql.Query(`SELECT version FROM sew_migrations ORDER BY applied_at ASC, version ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (d *DB) appliedSet() (map[string]bool, error) {
	vs, err := d.AppliedVersions()
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m, nil
}

// StatusRow pairs a migration with whether it's applied.
type StatusRow struct {
	Migration Migration
	Applied   bool
}

// Status reports each migration's applied state, in version order.
func (d *DB) Status(ms []Migration) ([]StatusRow, error) {
	applied, err := d.appliedSet()
	if err != nil {
		return nil, err
	}
	out := make([]StatusRow, 0, len(ms))
	for _, m := range ms {
		out = append(out, StatusRow{Migration: m, Applied: applied[m.Version]})
	}
	return out, nil
}

// Pending returns migrations not yet applied, in version order.
func (d *DB) Pending(ms []Migration) ([]Migration, error) {
	applied, err := d.appliedSet()
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, m := range ms {
		if !applied[m.Version] {
			out = append(out, m)
		}
	}
	return out, nil
}

// Up applies every pending migration in order. Each runs in its own transaction with
// its ledger insert, so a failure leaves earlier migrations committed and the failing
// one fully rolled back. DryRun reports the plan and changes nothing.
func (d *DB) Up(ms []Migration, opt Options) (Result, error) {
	pending, err := d.Pending(ms)
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, m := range pending {
		opt.log("up   %s — %s%s", m.Version, m.Name, dry(opt.DryRun))
		if opt.DryRun {
			res.Applied = append(res.Applied, m)
			continue
		}
		if err := d.runTx(m.Up, func(tx *sql.Tx) error {
			_, e := tx.Exec(`INSERT INTO sew_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				m.Version, m.Name, opt.now())
			return e
		}); err != nil {
			return res, fmt.Errorf("sew: up %s (%s): %w", m.Version, m.Path, err)
		}
		res.Applied = append(res.Applied, m)
	}
	return res, nil
}

// Down rolls back the last n applied migrations (n<=0 means 1), newest first, running
// each Down section. A migration with no Down section is an error (irreversible).
func (d *DB) Down(ms []Migration, n int, opt Options) (Result, error) {
	if n <= 0 {
		n = 1
	}
	byVersion := map[string]Migration{}
	for _, m := range ms {
		byVersion[m.Version] = m
	}
	appliedOrder, err := d.AppliedVersions()
	if err != nil {
		return Result{}, err
	}
	var res Result
	// newest first
	for i := len(appliedOrder) - 1; i >= 0 && len(res.Rolled) < n; i-- {
		v := appliedOrder[i]
		m, ok := byVersion[v]
		if !ok {
			return res, fmt.Errorf("sew: cannot roll back %s — its migration file is missing", v)
		}
		if strings.TrimSpace(m.Down) == "" {
			return res, fmt.Errorf("sew: cannot roll back %s — no `-- +sew down` section", v)
		}
		opt.log("down %s — %s%s", m.Version, m.Name, dry(opt.DryRun))
		if opt.DryRun {
			res.Rolled = append(res.Rolled, m)
			continue
		}
		if err := d.runTx(m.Down, func(tx *sql.Tx) error {
			_, e := tx.Exec(`DELETE FROM sew_migrations WHERE version = ?`, v)
			return e
		}); err != nil {
			return res, fmt.Errorf("sew: down %s (%s): %w", m.Version, m.Path, err)
		}
		res.Rolled = append(res.Rolled, m)
	}
	return res, nil
}

// runTx runs a migration body plus a ledger mutation atomically.
func (d *DB) runTx(body string, ledger func(*sql.Tx) error) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	for _, stmt := range splitStatements(body) {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("statement %q: %w", firstWords(stmt), err)
		}
	}
	if err := ledger(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Load reads every *.sql file in dir, parses it, and returns them sorted by version.
func Load(dir string) ([]Migration, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("sew: read %q: %w", p, err)
		}
		m := Parse(filepath.Base(p), string(data))
		m.Path = p
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	// Reject duplicate versions — an ambiguous order is a bug, not a warning.
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("sew: duplicate migration version %q (%s and %s)",
				out[i].Version, out[i-1].Path, out[i].Path)
		}
	}
	return out, nil
}

// Parse turns a file name + contents into a Migration. The version is the name without
// its .sql suffix; the human Name is the part after a leading "<token>_" prefix. Content
// is split on the `-- +sew up` / `-- +sew down` markers; a file with no markers is all-up.
func Parse(fileName, content string) Migration {
	version := strings.TrimSuffix(fileName, ".sql")
	name := version
	if i := strings.IndexByte(version, '_'); i >= 0 {
		name = strings.ReplaceAll(version[i+1:], "_", " ")
	}
	up, down := splitSections(content)
	return Migration{Version: version, Name: name, Up: strings.TrimSpace(up), Down: strings.TrimSpace(down)}
}

// splitSections divides content into the up and down bodies by the sew markers. Marker
// detection is line-based and case-insensitive on the directive; text before any marker
// is treated as up (so an unmarked file just works).
func splitSections(content string) (up, down string) {
	var ub, db strings.Builder
	cur := &ub // default: everything is "up" until a marker says otherwise
	for _, line := range strings.Split(content, "\n") {
		switch markerDirective(line) {
		case "+sew up":
			cur = &ub
			continue
		case "+sew down":
			cur = &db
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	return ub.String(), db.String()
}

// markerDirective returns the normalized sew directive on a comment line, or "".
// Recognizes "-- +sew up" / "-- +sew down" (any spacing, any case for up/down).
func markerDirective(line string) string {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "--") {
		return ""
	}
	t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "--")))
	switch t {
	case "+sew up", "+sew down":
		return t
	}
	return ""
}

func dry(b bool) string {
	if b {
		return "  (dry-run)"
	}
	return ""
}
