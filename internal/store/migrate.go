// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// columns added after the first release: what an older file lacks. A row from before has the default: no project key on
// an exchange (it is matched by its name), the global scope on a note and on a link (it is seen by every project, as it
// was), and not archived on an exchange. Nothing is rewritten, so an older version of the file's rows read the same.
var addedColumns = []struct{ table, col, ddl string }{
	{"exchanges", "pkey", "TEXT NOT NULL DEFAULT ''"},
	{"kv", "expires", "TEXT NOT NULL DEFAULT ''"},
	{"kv", "body", "TEXT NOT NULL DEFAULT ''"},
	{"kv", "project", "TEXT NOT NULL DEFAULT ''"},
	{"kv_link", "project", "TEXT NOT NULL DEFAULT ''"},
	{"exchanges", "archived", "INTEGER NOT NULL DEFAULT 0"},
}

// migrate brings a file written by an older version up to the schema: each missing column is added, in one transaction, so
// an interrupted run leaves the file as it was and a repeat does nothing.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	// Looked at without the write lock: a file that is up to date (nearly every open) must not wait for it, or a hook
	// that gives the lock a second would lose its event whenever the server is writing.
	missing := false
	for _, c := range addedColumns {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.col).Scan(&n); err != nil {
			return err
		}
		missing = missing || n == 0
	}
	if !missing {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil) // the writer pool begins IMMEDIATE: two processes migrating take turns; each column is looked at again under the lock
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range addedColumns {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.col).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, c.table, c.col, c.ddl)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
