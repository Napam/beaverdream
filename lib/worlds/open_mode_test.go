package worlds

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestExistingWorldOpenDoesNotCreateMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing ? world.db")
	db, err := sql.Open("sqlite", worldDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err == nil {
		t.Fatal("opening missing world succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("world file exists: %v", err)
	}
}
