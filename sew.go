// Package sew is a small, general-purpose SQLite migration runner: point it at any
// SQLite database and a directory of `.sql` migration files, and it applies the
// pending ones in order, once, recording what it applied so re-running is a no-op.
// Rollbacks run each migration's `down` section. No ORM, no codegen, no framework —
// read migrations, apply them, sew it up.
//
// The core owns NO SQLite driver: it runs over an injected Conn (see Conn/TxConn), so it
// compiles to wasm and can run as a Pulp cell that gets SQLite from the host capability,
// exactly like a native binary gets it from modernc. Use Open (native, CLI) to get a
// modernc-backed DB, or New(conn) to inject any Conn — e.g. a host-backed one in a cell.
//
// A migration file is plain SQL with two sections:
//
//	-- +sew up
//	CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT);
//
//	-- +sew down
//	DROP TABLE widgets;
//
// The file name (without .sql) is its VERSION and sort key: 001_init.sql, 20260708_x.sql.
package sew

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Conn is the minimal SQLite capability sew runs over. Inject a native (modernc) conn via
// Open, or a Pulp host-backed conn in a cell — the core imports no driver, so it builds to
// wasm. Query returns rows as [][]any (one []any per row), matching the host wire shape.
type Conn interface {
	Exec(query string, args ...any) error
	Query(query string, args ...any) ([][]any, error)
}

// TxConn is a Conn that can run a unit of work atomically. Backends that support
// transactions (the native modernc adapter) implement it, so each migration is
// all-or-nothing; a backend that doesn't (a bare host capability) is driven best-effort
// — statements then ledger, no rollback — which is how a seam-only store already migrates.
type TxConn interface {
	Conn
	RunTx(fn func(Conn) error) error
}

// Migration is one versioned step: its Up SQL, optional Down SQL, and identity.
type Migration struct {
	Version string // sort key / unique id (the file name without .sql)
	Name    string // human label (the part after the leading version token)
	Up      string // SQL applied when migrating forward
	Down    string // SQL applied when rolling back ("" = irreversible)
	Path    string // source file, for error messages ("" for in-memory)
}

// ledgerDDL records which versions have been applied. Created on first use.
const ledgerDDL = `CREATE TABLE IF NOT EXISTS sew_migrations (
	version    TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	applied_at INTEGER NOT NULL DEFAULT 0
)`

// Options tune an Up/Down run.
type Options struct {
	DryRun bool                          // plan only — writes nothing
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
	Applied []Migration // migrations whose Up ran
	Rolled  []Migration // migrations whose Down ran
}

// DB is a migration target: any Conn with the sew ledger ensured.
type DB struct{ conn Conn }

// New wraps an injected Conn (native, host-backed, or a test fake) and ensures the ledger
// table exists. This is the driver-agnostic constructor; Open is the native convenience.
func New(conn Conn) (*DB, error) {
	if err := conn.Exec(ledgerDDL); err != nil {
		return nil, fmt.Errorf("sew: init ledger: %w", err)
	}
	return &DB{conn: conn}, nil
}

// Close closes the underlying Conn if it is a Closer (the native adapter is); a
// host-backed Conn the caller owns is left untouched.
func (d *DB) Close() error {
	if c, ok := d.conn.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

// atomically runs fn in a transaction when the Conn supports one, else best-effort.
func (d *DB) atomically(fn func(Conn) error) error {
	if tx, ok := d.conn.(TxConn); ok {
		return tx.RunTx(fn)
	}
	return fn(d.conn)
}

// MarkApplied records a version as already applied WITHOUT running it — the hook a caller
// uses to adopt sew over a database migrated by an older mechanism (seed the ledger from a
// prior version counter so those steps don't re-run). Idempotent.
func (d *DB) MarkApplied(version, name string, at int64) error {
	return d.conn.Exec(
		`INSERT OR IGNORE INTO sew_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		version, name, at)
}

// AppliedVersions returns the applied versions in application order.
func (d *DB) AppliedVersions() ([]string, error) {
	rows, err := d.conn.Query(`SELECT version FROM sew_migrations ORDER BY applied_at ASC, version ASC`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, toString(r[0]))
		}
	}
	return out, nil
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

// Up applies every pending migration in order. Each runs atomically when the Conn is a
// TxConn (native), together with its ledger insert. DryRun reports the plan, changes nothing.
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
		if err := d.atomically(func(c Conn) error {
			if err := execAll(c, m.Up); err != nil {
				return err
			}
			return c.Exec(`INSERT INTO sew_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				m.Version, m.Name, opt.now())
		}); err != nil {
			return res, fmt.Errorf("sew: up %s (%s): %w", m.Version, m.Path, err)
		}
		res.Applied = append(res.Applied, m)
	}
	return res, nil
}

// Down rolls back the last n applied migrations (n<=0 means 1), newest first. A migration
// with no Down section is an error (irreversible).
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
		if err := d.atomically(func(c Conn) error {
			if err := execAll(c, m.Down); err != nil {
				return err
			}
			return c.Exec(`DELETE FROM sew_migrations WHERE version = ?`, v)
		}); err != nil {
			return res, fmt.Errorf("sew: down %s (%s): %w", m.Version, m.Path, err)
		}
		res.Rolled = append(res.Rolled, m)
	}
	return res, nil
}

// execAll runs each statement in a migration body over c.
func execAll(c Conn, body string) error {
	for _, stmt := range splitStatements(body) {
		if err := c.Exec(stmt); err != nil {
			return fmt.Errorf("statement %q: %w", firstWords(stmt), err)
		}
	}
	return nil
}

// toString coerces a scanned cell (string or []byte, depending on the backend) to string.
func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
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
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("sew: duplicate migration version %q (%s and %s)",
				out[i].Version, out[i-1].Path, out[i].Path)
		}
	}
	return out, nil
}

// Parse turns a file name + contents into a Migration.
func Parse(fileName, content string) Migration {
	version := strings.TrimSuffix(fileName, ".sql")
	name := version
	if i := strings.IndexByte(version, '_'); i >= 0 {
		name = strings.ReplaceAll(version[i+1:], "_", " ")
	}
	up, down := splitSections(content)
	return Migration{Version: version, Name: name, Up: strings.TrimSpace(up), Down: strings.TrimSpace(down)}
}

// splitSections divides content into up/down bodies by the sew markers.
func splitSections(content string) (up, down string) {
	var ub, db strings.Builder
	cur := &ub
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
