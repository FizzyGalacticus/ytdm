package storage

import (
	"fmt"
	"strings"
)

// copyTables lists every application table in foreign-key dependency order (parents
// first). schema_migrations is deliberately absent: each database keeps its own record of
// which of its dialect's migrations it has applied.
var copyTables = []string{
	"sources",
	"channels",
	"channel_sources",
	"videos",
	"video_sources",
	"channel_videos",
	"individual_video_tracking",
	"json_import_state",
}

// identityTables are the tables whose id column auto-increments on PostgreSQL and so
// needs its sequence moved past the copied ids.
var identityTables = []string{"channels", "videos"}

// CopyResult reports how many rows CopyDatabase copied per table, in copy order.
type CopyResult struct {
	Tables []TableCopyCount `json:"tables"`
}

// TableCopyCount is one table's row counts in a CopyResult. Rows is how many source
// rows were processed; Inserted and Updated split them by what happened in the
// destination (a replace inserts everything).
type TableCopyCount struct {
	Table    string `json:"table"`
	Rows     int64  `json:"rows"`
	Inserted int64  `json:"inserted"`
	Updated  int64  `json:"updated"`
}

// CopyDatabase copies every row from src into dst, preserving primary keys, so a SQLite
// database can be moved to PostgreSQL or back. Both must be at the same schema version
// (opening each with the same ytdm binary guarantees that). dst must hold no channels or
// videos unless overwrite is set, in which case its existing rows are deleted first. The
// whole copy runs in one dst transaction and is checked row-for-row before committing,
// so a failure leaves dst exactly as it was. src is only read.
func CopyDatabase(src, dst *Storage, overwrite bool) (CopyResult, error) {
	var result CopyResult

	if err := checkSameSchema(src, dst); err != nil {
		return result, err
	}

	if !overwrite {
		var count int64
		if err := dst.db.QueryRow(`SELECT (SELECT COUNT(*) FROM channels) + (SELECT COUNT(*) FROM videos)`).Scan(&count); err != nil {
			return result, fmt.Errorf("checking destination is empty: %w", err)
		}
		if count > 0 {
			return result, fmt.Errorf("destination database already holds %d channel/video row(s); re-run with overwrite to replace them", count)
		}
	}

	tx, err := dst.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	for i := len(copyTables) - 1; i >= 0; i-- {
		if _, err := tx.Exec(`DELETE FROM ` + copyTables[i]); err != nil {
			return result, fmt.Errorf("clearing destination table %s: %w", copyTables[i], err)
		}
	}

	for _, table := range copyTables {
		n, err := copyTable(src.db, tx, table)
		if err != nil {
			return result, fmt.Errorf("copying table %s: %w", table, err)
		}
		result.Tables = append(result.Tables, TableCopyCount{Table: table, Rows: n, Inserted: n})
	}

	if tx.dialect == DialectPostgres {
		for _, table := range identityTables {
			if _, err := tx.Exec(fmt.Sprintf(
				`SELECT setval(pg_get_serial_sequence('%[1]s', 'id'), COALESCE((SELECT MAX(id) FROM %[1]s), 0) + 1, false)`,
				table)); err != nil {
				return result, fmt.Errorf("resetting %s id sequence: %w", table, err)
			}
		}
	}

	for _, tc := range result.Tables {
		var n int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + tc.Table).Scan(&n); err != nil {
			return result, fmt.Errorf("verifying %s: %w", tc.Table, err)
		}
		if n != tc.Rows {
			return result, fmt.Errorf("verifying %s: copied %d row(s) but destination has %d", tc.Table, tc.Rows, n)
		}
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}
	dst.notify()
	return result, nil
}

func schemaVersion(db *dbConn) (int, error) {
	var v int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// copyTable streams every row of table from src into the dst transaction, naming
// columns explicitly so column order may differ between the two databases.
func copyTable(src *dbConn, dst *dbTx, table string) (int64, error) {
	rows, err := src.Query(`SELECT * FROM ` + table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	stmt, err := dst.Prepare(fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		table, strings.Join(cols, ", "), placeholders))
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}

	var n int64
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		for i, v := range values {
			// Every column is INTEGER or TEXT on both backends; a driver handing TEXT
			// back as bytes must not be inserted as a binary value.
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		if _, err := stmt.Exec(values...); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}
