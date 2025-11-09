package repo

import (
	"context"
	"database/sql"
	"errors"
)

// ErrNilDB indicates the repository was initialized without a *sql.DB handle.
var ErrNilDB = errors.New("repo: nil db")

// withTx executes fn inside a database transaction, committing on success and
// rolling back on error. It centralizes the boilerplate used by repositories
// that need atomic multi-table writes.
func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	if db == nil {
		return ErrNilDB
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
