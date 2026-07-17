package sow

import "strconv"

// dialect.go — the SQL-flavour seam. sow's runner logic is dialect-free: every statement it
// issues against its own ledger table is rendered here, so teaching sow a new database means
// adding a Dialect value, not editing Up/Down/MarkApplied.
//
// Only three things actually vary, so only three things live here:
//   - the ledger DDL,
//   - how an insert says "ignore a duplicate version",
//   - how a bind parameter is spelled.
//
// Migration bodies themselves are passed through untouched — they are the caller's SQL, in
// the caller's dialect, and sow has never rewritten them.

// Dialect is the SQL flavour sow speaks to a target database. sow ships two: SQLite (the
// default) and Postgres. The zero value is SQLite, so a DB built without naming a dialect
// behaves exactly as sow did before dialects existed.
//
// Its fields are unexported on purpose: a Dialect is a value sow hands you (SQLite() or
// Postgres()), not a plugin point. That keeps the published surface two functions wide and
// leaves sow free to change what a dialect must know without breaking callers.
type Dialect struct {
	name string // "sqlite" | "postgres" — identity, for errors and tests

	// ledgerDDL creates sow_migrations. Both shipped dialects use the same portable DDL
	// today; this is the seam a third dialect would vary (e.g. one with no TEXT PRIMARY
	// KEY) without the runner learning about it.
	ledgerDDL string

	// insertVerb opens an insert that must not fail when the version is already recorded,
	// and onConflict closes it. SQLite says so up front ("INSERT OR IGNORE INTO …");
	// Postgres says so at the end ("INSERT INTO … ON CONFLICT (version) DO NOTHING").
	// Splitting it in two is what lets one template serve both shapes.
	insertVerb string
	onConflict string

	// placeholder renders the n-th (1-based) bind parameter: "?" on SQLite, "$n" on Postgres.
	placeholder func(n int) string
}

// SQLite returns the SQLite dialect: `INSERT OR IGNORE` and `?` placeholders. This is what
// Open and New use, and what the zero Dialect resolves to.
func SQLite() Dialect { return sqliteDialect }

// Postgres returns the Postgres dialect: `ON CONFLICT (version) DO NOTHING` and `$n`
// placeholders. Pair it with OpenPostgres, or with NewWithDialect over an injected Conn.
func Postgres() Dialect { return postgresDialect }

var sqliteDialect = Dialect{
	name:        "sqlite",
	ledgerDDL:   ledgerDDL,
	insertVerb:  "INSERT OR IGNORE INTO",
	onConflict:  "",
	placeholder: func(int) string { return "?" },
}

var postgresDialect = Dialect{
	name:        "postgres",
	ledgerDDL:   ledgerDDL,
	insertVerb:  "INSERT INTO",
	onConflict:  " ON CONFLICT (version) DO NOTHING",
	placeholder: func(n int) string { return "$" + strconv.Itoa(n) },
}

// resolve maps the zero Dialect onto SQLite, so `Dialect{}` is a usable default rather than
// a source of empty SQL. Called once at construction; the DB stores the resolved value.
func (d Dialect) resolve() Dialect {
	if d.placeholder == nil {
		return sqliteDialect
	}
	return d
}

// String names the dialect, for error messages.
func (d Dialect) String() string { return d.resolve().name }

// ph renders the n-th (1-based) bind placeholder.
func (d Dialect) ph(n int) string { return d.placeholder(n) }

// ledgerCols is the ledger's column list and value template, shared by both inserts.
const ledgerCols = " sow_migrations (version, name, applied_at) VALUES ("

// insertLedgerSQL records a freshly applied version. A duplicate here is a bug (Up only
// runs pending migrations), so this insert is strict — it is not an upsert.
func (d Dialect) insertLedgerSQL() string {
	return "INSERT INTO" + ledgerCols + d.ph(1) + ", " + d.ph(2) + ", " + d.ph(3) + ")"
}

// upsertLedgerSQL records a version and ignores it if already present — MarkApplied's
// idempotence.
func (d Dialect) upsertLedgerSQL() string {
	return d.insertVerb + ledgerCols + d.ph(1) + ", " + d.ph(2) + ", " + d.ph(3) + ")" + d.onConflict
}

// deleteLedgerSQL forgets a version, after its Down section has run.
func (d Dialect) deleteLedgerSQL() string {
	return "DELETE FROM sow_migrations WHERE version = " + d.ph(1)
}
