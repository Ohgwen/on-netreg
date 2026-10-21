// Package db owns the gorm connection and schema for the registry database.
// It supports SQLite (default) and Postgres behind the same models, selected
// via config.Database.Driver.
package db

import (
	"fmt"
	"log"
	"os"
	"strings"
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
		dialector = sqlite.Open(sqliteDSN(cfg.DSN))
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

// sqlitePragmas are applied to every pooled connection. WAL lets page
// loads read while the sync loop's write transaction is open instead of
// waiting for it; busy_timeout makes concurrent writers wait for the lock
// rather than fail; synchronous=NORMAL is the recommended, still
// crash-safe, setting under WAL. In-memory databases ignore journal_mode.
var sqlitePragmas = []string{"journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)"}

// sqliteDSN adds sqlitePragmas to dsn, leaving any pragma the DSN already
// sets alone.
func sqliteDSN(dsn string) string {
	var extra []string
	for _, p := range sqlitePragmas {
		name := p[:strings.IndexByte(p, '(')]
		if !strings.Contains(dsn, "_pragma="+name) {
			extra = append(extra, "_pragma="+p)
		}
	}
	if len(extra) == 0 {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + strings.Join(extra, "&")
}

// Migrate applies schema migrations. AutoMigrate works unchanged across the
// sqlite and postgres dialects for the models used here.
func Migrate(gdb *gorm.DB) error {
	if err := gdb.AutoMigrate(
		&Device{}, &SyncEvent{},
		&UnifiController{}, &UnifiNetwork{}, &TechnitiumSettings{}, &AppSettings{},
		&Identity{}, &IdentityMember{}, &DeviceAlias{},
	); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	// Superseded by idx_alias_device_label_zone (aliases can now be pinned
	// to a zone); the old two-column unique index would wrongly forbid the
	// same label in two zones.
	if gdb.Migrator().HasIndex(&DeviceAlias{}, "idx_alias_device_label") {
		if err := gdb.Migrator().DropIndex(&DeviceAlias{}, "idx_alias_device_label"); err != nil {
			return fmt.Errorf("dropping old alias index: %w", err)
		}
	}
	return nil
}
