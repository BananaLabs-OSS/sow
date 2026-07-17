// Command sow is a general-purpose SQLite and PostgreSQL migration runner.
//
//	sow up     <db> <dir> [--dry-run]     apply all pending migrations
//	sow down   <db> <dir> [--steps N]     roll back the last N applied (default 1)
//	sow status <db> <dir>                 show applied / pending
//	sow create <dir> <name>               scaffold a new timestamped migration file
//	sow version
//
// <db> is a SQLite path (or :memory:), a Postgres URL (postgres://…), or `-` to
// read the DSN from $DATABASE_URL / $PGURL. Prefer the env form for Postgres: a
// DSN carries a password, and an argument is visible to `ps` and shell history.
//
// A migration is a .sql file with `-- +sow up` / `-- +sow down` sections. The file name
// (without .sql) is its version and sort key; use a zero-padded or timestamp prefix.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SirNiklas9/sow"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "up":
		cmdUp(rest)
	case "down":
		cmdDown(rest)
	case "status":
		cmdStatus(rest)
	case "create", "new":
		cmdCreate(rest)
	case "version", "--version", "-v":
		fmt.Println("sow " + version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "sow: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

var version = "dev" // stamped via -ldflags "-X main.version=$(git describe --tags)"

// flags pulls simple `--name` / `--name value` flags out of args, returning the
// positional remainder. Keeps the CLI dependency-free.
func takeFlag(args []string, name string) (val string, present bool, rest []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--"+name {
			present = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				val = args[i+1]
				rest = append(append([]string{}, args[:i]...), args[i+2:]...)
			} else {
				rest = append(append([]string{}, args[:i]...), args[i+1:]...)
			}
			return
		}
	}
	return "", false, args
}

func loadTarget(args []string) (*sow.DB, []sow.Migration, []string) {
	if len(args) < 2 {
		die("need <db> <dir>")
	}
	target, dir := args[0], args[1]
	migs, err := sow.Load(dir)
	if err != nil {
		die("%v", err)
	}
	db, err := openTarget(target)
	if err != nil {
		// openTarget already redacts; still, never let a raw DSN reach here.
		die("%v", err)
	}
	return db, migs, args[2:]
}

// openTarget routes to the right backend by the shape of the target string. A
// Postgres URL (postgres:// or postgresql://) opens Postgres; anything else is
// a SQLite path, exactly as before. `-` or an empty target means "read the DSN
// from $DATABASE_URL / $PGURL" — the recommended form, because a DSN carries a
// password and an argv is visible to `ps` and lands in shell history.
func openTarget(target string) (*sow.DB, error) {
	if target == "-" || target == "" {
		dsn := firstEnv("DATABASE_URL", "PGURL")
		if dsn == "" {
			die("no DSN: set $DATABASE_URL or $PGURL (or pass a sqlite path)")
		}
		return sow.OpenPostgres(dsn)
	}
	if isPostgresDSN(target) {
		return sow.OpenPostgres(target)
	}
	return sow.Open(target)
}

// isPostgresDSN recognizes the two standard URL schemes. A bare key=value DSN
// (host=… user=…) is deliberately NOT auto-detected: it is ambiguous with a
// filename and rare from a CLI. Use the env-var form for those.
func isPostgresDSN(s string) bool {
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func cmdUp(args []string) {
	_, dry, args := takeFlag(args, "dry-run")
	db, migs, _ := loadTarget(args)
	defer db.Close()
	res, err := db.Up(migs, sow.Options{DryRun: dry, Logf: logf, Now: nowMilli})
	if err != nil {
		die("%v", err)
	}
	if len(res.Applied) == 0 {
		fmt.Println("up to date — nothing pending")
		return
	}
	fmt.Printf("applied %d migration(s)%s\n", len(res.Applied), suffix(dry))
}

func cmdDown(args []string) {
	stepsStr, _, args := takeFlag(args, "steps")
	_, dry, args := takeFlag(args, "dry-run")
	steps := 1
	if stepsStr != "" {
		if n, err := strconv.Atoi(stepsStr); err == nil {
			steps = n
		}
	}
	db, migs, _ := loadTarget(args)
	defer db.Close()
	res, err := db.Down(migs, steps, sow.Options{DryRun: dry, Logf: logf, Now: nowMilli})
	if err != nil {
		die("%v", err)
	}
	fmt.Printf("rolled back %d migration(s)%s\n", len(res.Rolled), suffix(dry))
}

func cmdStatus(args []string) {
	db, migs, _ := loadTarget(args)
	defer db.Close()
	rows, err := db.Status(migs)
	if err != nil {
		die("%v", err)
	}
	pending := 0
	for _, r := range rows {
		mark := "  applied"
		if !r.Applied {
			mark = "· pending"
			pending++
		}
		fmt.Printf("%s  %-24s %s\n", mark, r.Migration.Version, r.Migration.Name)
	}
	fmt.Printf("\n%d migration(s), %d pending\n", len(rows), pending)
}

func cmdCreate(args []string) {
	if len(args) < 2 {
		die("usage: sow create <dir> <name>")
	}
	dir, name := args[0], sanitize(args[1])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		die("%v", err)
	}
	stamp := time.Now().UTC().Format("20060102150405")
	path := filepath.Join(dir, stamp+"_"+name+".sql")
	if _, err := os.Stat(path); err == nil {
		die("already exists: %s", path)
	}
	content := "-- +sow up\n\n\n-- +sow down\n\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		die("%v", err)
	}
	fmt.Println("created", path)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func nowMilli() int64 { return time.Now().UnixMilli() }

func logf(format string, a ...any) { fmt.Printf("  "+format+"\n", a...) }

func suffix(dry bool) string {
	if dry {
		return " (dry-run — nothing written)"
	}
	return ""
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "sow: "+format+"\n", a...)
	os.Exit(1)
}

func usage() {
	fmt.Print(`sow, a small SQLite and PostgreSQL migration runner

Usage:
  sow up     <db> <dir> [--dry-run]     apply all pending migrations
  sow down   <db> <dir> [--steps N]     roll back the last N applied (default 1)
  sow status <db> <dir>                 show applied / pending
  sow create <dir> <name>               scaffold a new timestamped migration file
  sow version

<db> is a SQLite path (or :memory:), a Postgres URL (postgres://...), or '-' to
read the DSN from $DATABASE_URL or $PGURL. Prefer the env form for Postgres:
a DSN carries a password, and an argument is visible to ps and shell history.

A migration is a .sql file with two sections:
  -- +sow up
  CREATE TABLE t (id INTEGER PRIMARY KEY);
  -- +sow down
  DROP TABLE t;

The file name without .sql is the version and sort key (use 001_… or a timestamp).
`)
}
