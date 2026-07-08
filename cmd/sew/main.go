// Command sew is a general-purpose SQLite migration runner.
//
//	sew up     <db> <dir> [--dry-run]     apply all pending migrations
//	sew down   <db> <dir> [--steps N]     roll back the last N applied (default 1)
//	sew status <db> <dir>                 show applied / pending
//	sew create <dir> <name>               scaffold a new timestamped migration file
//	sew version
//
// A migration is a .sql file with `-- +sew up` / `-- +sew down` sections. The file name
// (without .sql) is its version and sort key; use a zero-padded or timestamp prefix.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SirNiklas9/sew"
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
		fmt.Println("sew " + version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "sew: unknown command %q\n\n", cmd)
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

func loadTarget(args []string) (*sew.DB, []sew.Migration, []string) {
	if len(args) < 2 {
		die("need <db> <dir>")
	}
	dbPath, dir := args[0], args[1]
	migs, err := sew.Load(dir)
	if err != nil {
		die("%v", err)
	}
	db, err := sew.Open(dbPath)
	if err != nil {
		die("%v", err)
	}
	return db, migs, args[2:]
}

func cmdUp(args []string) {
	_, dry, args := takeFlag(args, "dry-run")
	db, migs, _ := loadTarget(args)
	defer db.Close()
	res, err := db.Up(migs, sew.Options{DryRun: dry, Logf: logf, Now: nowMilli})
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
	res, err := db.Down(migs, steps, sew.Options{DryRun: dry, Logf: logf, Now: nowMilli})
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
		die("usage: sew create <dir> <name>")
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
	content := "-- +sew up\n\n\n-- +sew down\n\n"
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
	fmt.Fprintf(os.Stderr, "sew: "+format+"\n", a...)
	os.Exit(1)
}

func usage() {
	fmt.Print(`sew — a small SQLite migration runner

Usage:
  sew up     <db> <dir> [--dry-run]     apply all pending migrations
  sew down   <db> <dir> [--steps N]     roll back the last N applied (default 1)
  sew status <db> <dir>                 show applied / pending
  sew create <dir> <name>               scaffold a new timestamped migration file
  sew version

A migration is a .sql file with two sections:
  -- +sew up
  CREATE TABLE t (id INTEGER PRIMARY KEY);
  -- +sew down
  DROP TABLE t;

The file name without .sql is the version and sort key (use 001_… or a timestamp).
`)
}
