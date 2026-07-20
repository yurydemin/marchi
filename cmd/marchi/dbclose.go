package main

import (
	"database/sql"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/db"
)

// closeDB wraps db.Close (WAL checkpoint + close, NFR-RL-05) for use in a
// defer, logging rather than swallowing a failure — every command already
// has a logger on its context by the time it opens a database connection.
func closeDB(logger *zap.Logger, sqlDB *sql.DB) {
	if err := db.Close(sqlDB); err != nil {
		logger.Warn("closing database", zap.Error(err))
	}
}
