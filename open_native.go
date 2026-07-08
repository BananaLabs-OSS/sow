//go:build !wasm

package sew

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite" (no CGo)
)

// open_native.go is the NATIVE adapter: a modernc-backed Conn for a plain Go binary
// (the CLI, tests, any non-wasm caller). It is excluded from wasm builds — there a cell
// injects a host-backed Conn via New() instead, so the sew core carries no SQLite engine.

// Open opens (or creates) a SQLite database at path (":memory:" allowed) and returns a DB
// with the ledger ensured, backed by modernc over database/sql.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sew: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1) // one connection keeps :memory: coherent and cross-call tx valid
	d, err := New(&sqliteConn{db: db})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return d, nil
}

// sqliteConn is a native database/sql connection satisfying Conn, TxConn, and io.Closer.
type sqliteConn struct{ db *sql.DB }

func (c *sqliteConn) Exec(query string, args ...any) error {
	_, err := c.db.Exec(query, args...)
	return err
}

func (c *sqliteConn) Query(query string, args ...any) ([][]any, error) {
	rows, err := c.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

func (c *sqliteConn) Close() error { return c.db.Close() }

// RunTx runs fn inside a transaction, committing on success and rolling back on error.
func (c *sqliteConn) RunTx(fn func(Conn) error) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(&txConn{tx: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// txConn is a transaction-scoped Conn handed to a RunTx body.
type txConn struct{ tx *sql.Tx }

func (t *txConn) Exec(query string, args ...any) error {
	_, err := t.tx.Exec(query, args...)
	return err
}

func (t *txConn) Query(query string, args ...any) ([][]any, error) {
	rows, err := t.tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// scanRows reads all rows into [][]any (one []any per row), matching Conn.Query's shape.
func scanRows(rows *sql.Rows) ([][]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]any
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, cells)
	}
	return out, rows.Err()
}
