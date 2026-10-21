package db

import (
	"path/filepath"
	"testing"

	"github.com/Ohgwen/on-netreg/internal/config"
)

func TestSQLiteDSNAddsPragmas(t *testing.T) {
	for dsn, want := range map[string]string{
		"/data/netreg.db": "/data/netreg.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		"file:x?mode=memory&_pragma=busy_timeout(100)": "file:x?mode=memory&_pragma=busy_timeout(100)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
	} {
		if got := sqliteDSN(dsn); got != want {
			t.Errorf("sqliteDSN(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestOpenSQLiteFileUsesWAL(t *testing.T) {
	gdb, err := Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "netreg.db")})
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	var timeout int
	gdb.Raw("PRAGMA journal_mode").Scan(&mode)
	gdb.Raw("PRAGMA busy_timeout").Scan(&timeout)
	if mode != "wal" || timeout != 5000 {
		t.Errorf("journal_mode=%q busy_timeout=%d, want wal and 5000", mode, timeout)
	}
}
