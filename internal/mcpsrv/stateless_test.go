package mcpsrv

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/beetlebugorg/resumed/internal/app"
	"github.com/beetlebugorg/resumed/internal/store"
)

// TestPerCallStoreHoldsNothingBetweenCalls covers the reason the middleware
// exists: a connection kept for the life of the process stops SQLite from
// checkpointing its write-ahead log into the database file.
func TestPerCallStoreHoldsNothingBetweenCalls(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	opens := 0
	reopen := func() (*store.Store, error) {
		opens++
		return store.Open(dbPath)
	}

	a := app.New(nil, t.TempDir())
	var sawStore bool
	next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		sawStore = a.Store != nil
		return nil, nil
	}
	handler := perCallStore(a, reopen)(next)

	for i := 1; i <= 3; i++ {
		if _, err := handler(context.Background(), "tools/call", nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !sawStore {
			t.Errorf("call %d ran without a store", i)
		}
		if a.Store != nil {
			t.Errorf("call %d left a connection open", i)
		}
		if opens != i {
			t.Errorf("call %d opened the database %d times", i, opens)
		}
	}
}

// TestPerCallStoreReportsOpenFailure checks that a database that cannot be
// opened fails the call rather than running a handler with no store.
func TestPerCallStoreReportsOpenFailure(t *testing.T) {
	a := app.New(nil, t.TempDir())
	ran := false
	next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		ran = true
		return nil, nil
	}
	// A directory is not a database file.
	handler := perCallStore(a, func() (*store.Store, error) { return store.Open(t.TempDir()) })(next)

	if _, err := handler(context.Background(), "tools/call", nil); err == nil {
		t.Error("want an error when the database cannot be opened")
	}
	if ran {
		t.Error("the handler ran despite the open failing")
	}
}
