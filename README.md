# sow

A small, general-purpose **SQLite migration runner** — a library and a CLI. Point it at
any SQLite database and a directory of `.sql` migration files; it applies the pending
ones in order, once, inside a transaction, and records what it applied so re-running is a
no-op. Rollbacks run each migration's `down` section. No ORM, no codegen, no framework.

Read migrations, apply them, sow it up.

## Migration files

A migration is a plain `.sql` file with two sections:

```sql
-- +sow up
CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT);

-- +sow down
DROP TABLE widgets;
```

The file name without `.sql` is its **version** and sort key, so use a zero-padded or
timestamp prefix to keep order deterministic:

```
migrations/
  001_create_widgets.sql
  002_add_index.sql
  20260708_backfill.sql
```

A file with no markers is treated as all-`up` (no rollback). Statements are split on
top-level `;` (quotes and `--` / `/* */` comments are respected).

## CLI

```
sow up     <db> <dir> [--dry-run]     apply all pending migrations
sow down   <db> <dir> [--steps N]     roll back the last N applied (default 1)
sow status <db> <dir>                 show applied / pending
sow create <dir> <name>               scaffold a new timestamped migration file
sow version
```

Example:

```sh
sow create ./migrations "create users"   # writes ./migrations/<ts>_create_users.sql
# ...edit the file...
sow status ./app.db ./migrations
sow up     ./app.db ./migrations
sow down   ./app.db ./migrations --steps 1
```

Install:

```sh
go install github.com/SirNiklas9/sow/cmd/sow@latest
```

## Library

```go
import "github.com/SirNiklas9/sow"

migs, _ := sow.Load("./migrations")
db, _   := sow.Open("./app.db")
defer db.Close()

res, err := db.Up(migs, sow.Options{Logf: log.Printf})
// db.Down(migs, 1, opt) · db.Status(migs) · db.Pending(migs) · db.AppliedVersions()
```

Applied versions are tracked in a `sow_migrations` table created automatically. Each
migration runs in its own transaction together with its ledger write, so a failure leaves
earlier migrations committed and the failing one fully rolled back.

## How it works

- **Pure Go** — uses `modernc.org/sqlite` (no CGo).
- **Idempotent** — a version already in `sow_migrations` is skipped.
- **Ordered** — migrations apply in version (filename) order; duplicate versions are an error.
- **Reversible** — `down` sections enable `sow down`; a migration with no `down` refuses to roll back.

## Limitations

- SQLite only.
- Statement splitting is on top-level `;`. A body containing an inner `;` (e.g. a trigger
  with `BEGIN … ; … ; END`) should be authored so that block is the file's only statement.

---

## License

[MIT](LICENSE) © 2026 SirNiklas9.
