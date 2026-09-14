package mcpsrv

import (
	"path/filepath"
	"testing"

	"github.com/beetlebugorg/resumed/internal/app"
	"github.com/beetlebugorg/resumed/internal/store"
)

// New registers every tool, and the SDK builds a JSON schema from each tool's
// input and output types. A type that refers to itself makes that generation
// panic, which takes the whole server down before it serves a single request.
// store.Bullet gained a Children field of its own type and did exactly that.
func TestNewRegistersEveryToolWithoutPanicking(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	a := app.New(st, t.TempDir())

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering tools panicked: %v", r)
		}
	}()
	if s := New(a, "test", func() (*store.Store, error) { return store.Open(dbPath) }); s == nil {
		t.Fatal("New returned no server")
	}
}
