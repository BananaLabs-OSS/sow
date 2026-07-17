//go:build !wasm

package sow

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx's database/sql driver, registered as "pgx"
)

// open_postgres.go is the NATIVE Postgres adapter, and it carries the SAME `//go:build !wasm`
// tag as open_native.go for a load-bearing reason: this file is the only thing in sow that
// imports a Postgres driver, and a Postgres driver must never be linked into a wasm build.
// A Pulp cell has no sockets — pgx there would be dead weight at best and a broken build at
// worst — and the whole cell path depends on the sow core importing no driver at all. The tag
// keeps that true: on wasm this file drops out, pgx leaves the build graph entirely, and a
// cell that wants Postgres injects a host-backed Conn via NewWithDialect(conn, Postgres()).

// OpenPostgres opens a Postgres database at dsn (a URL like
// "postgres://user:pass@host:5432/db?sslmode=require", or a libpq key/value string) and
// returns a DB with the ledger ensured, backed by pgx over database/sql. It is the Postgres
// sibling of Open; the DB it returns speaks Postgres SQL for its ledger.
//
// Note that sql.Open does not dial — a bad host surfaces on the ledger's first statement,
// which is inside this call, so a returned DB has proven it can reach the database.
func OpenPostgres(dsn string) (*DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("sow: open postgres: %w", err)
	}
	d, err := NewWithDialect(&sqlConn{db: db}, Postgres())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return d, nil
}
