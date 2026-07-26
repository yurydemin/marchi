package backup

import (
	"context"
	"database/sql"
	"fmt"

	sqlite "modernc.org/sqlite"
)

// backuper is the subset of modernc.org/sqlite's driver connection type
// reachable through sql.Conn.Raw(): the concrete type behind driver.Conn
// (an unexported *sqlite.conn) has an exported NewBackup method, so a
// local structural interface with the matching signature is enough to
// call it via a type assertion — the standard Go pattern for reaching
// driver-specific extensions through database/sql, since we can't name
// the unexported concrete type directly.
type backuper interface {
	NewBackup(dstURI string) (*sqlite.Backup, error)
}

// backupDatabase writes a consistent copy of sqlDB's current database to
// destPath using SQLite's Online Backup API (sqlite3_backup_init/step/
// finish under the hood, via modernc.org/sqlite's pure-Go binding — see
// backuper above). A single Step(-1) copies every remaining source page
// in one call; Marchi's archive sizes don't need the incremental,
// progress-reporting form of the API.
func backupDatabase(ctx context.Context, sqlDB *sql.DB, destPath string) error {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("backup: acquiring database connection: %w", err)
	}
	defer conn.Close()

	return conn.Raw(func(driverConn any) error {
		b, ok := driverConn.(backuper)
		if !ok {
			return fmt.Errorf("backup: sqlite driver connection does not support the Online Backup API (got %T)", driverConn)
		}
		bck, err := b.NewBackup(destPath)
		if err != nil {
			return fmt.Errorf("backup: starting online backup: %w", err)
		}
		if _, err := bck.Step(-1); err != nil {
			_ = bck.Finish()
			return fmt.Errorf("backup: copying database pages: %w", err)
		}
		if err := bck.Finish(); err != nil {
			return fmt.Errorf("backup: finishing online backup: %w", err)
		}
		return nil
	})
}
