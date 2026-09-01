// Package db owns the gorm connection and schema for the registry database.
// It supports SQLite (default) and Postgres behind the same models, selected
// via config.Database.Driver.
package db

import (
	"fmt"
	"log"
	"os"
	"time"

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
		// IgnoreRecordNotFoundError is set because the settings package
		// routinely does get-or-create lookups (e.g. the singleton
		// AppSettings/TechnitiumSettings rows before they're configured)
		// where a miss is an expected, not an error-worthy, outcome.
		Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
		}),
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
	if err := gdb.AutoMigrate(
		&Device{}, &SyncEvent{},
		&UnifiController{}, &UnifiNetwork{}, &TechnitiumSettings{}, &AppSettings{},
		&Identity{}, &IdentityMember{},
	); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}
