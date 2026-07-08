// Command sow-cell is a Pulp cell (GOARCH=wasm) that runs sow migrations against the
// host's storage.sqlite capability. It is the "migrator cell": the brain is pure sow
// logic; the SQLite is the host's. Drop it into a Pulp host alongside other cells and it
// brings the database up to schema on init — no driver in the cell, migrations embedded.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o cell.wasm .
//
// The cell must declare the "storage.sqlite" capability in its host manifest.
package main

import (
	"fmt"

	"embed"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/SirNiklas9/sow"
)

//go:embed migrations/*.sql
var migrations embed.FS

// pulpConn adapts the host's storage.sqlite capability to sow.Conn. It is not a TxConn —
// the host seam exposes exec/query only — so sow drives it best-effort, which is exactly
// how a schema migration over a bare capability should run.
type pulpConn struct{}

func (pulpConn) Exec(query string, args ...any) error {
	_, err := pulp.SQLite.Exec(query, args...)
	return err
}

func (pulpConn) Query(query string, args ...any) ([][]any, error) {
	res, err := pulp.SQLite.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

func init() {
	pulp.OnInit(func(_ []byte) error {
		db, err := sow.New(pulpConn{})
		if err != nil {
			return fmt.Errorf("sow: init: %w", err)
		}
		migs, err := sow.LoadFS(migrations, "migrations")
		if err != nil {
			return fmt.Errorf("sow: load embedded migrations: %w", err)
		}
		res, err := db.Up(migs, sow.Options{
			Logf: func(format string, a ...any) { fmt.Printf("[sow-cell] "+format+"\n", a...) },
		})
		if err != nil {
			return fmt.Errorf("sow: migrate: %w", err)
		}
		applied, _ := db.AppliedVersions()
		fmt.Printf("[sow-cell] migrated: +%d this run, %d total applied\n", len(res.Applied), len(applied))
		return nil
	})

	pulp.OnStep(func(pulp.StepEvent) error { return nil })
}

func main() {}
