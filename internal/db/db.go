// Package db owns the gorm connection and schema for the registry database.
// It supports SQLite (default) and Postgres behind the same models, selected
// via config.Database.Driver.
package db

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Ohgwen/on-netreg/internal/config"
)

// Open connects to the configured database and runs schema migrations.
func Open(cfg config.DatabaseConfig) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch cfg.Driver {
	case "sqlite":
		dialector = sqlite.Open(cfg.DSN)
	case "postgres":
		dialector = postgres.Open(cfg.DSN)
	default:
		return nil, fmt.Errorf("unsupported database driver %q", cfg.Driver)
	}

	gdb, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := Migrate(gdb); err != nil {
		return nil, err
	}

	return gdb, nil
}

// Migrate applies schema migrations. AutoMigrate works unchanged across the
// sqlite and postgres dialects for the models used here.
func Migrate(gdb *gorm.DB) error {
	if err := gdb.AutoMigrate(&Device{}, &SyncEvent{}); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}
