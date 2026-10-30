package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/recurso-dev/recurso/internal/adapter/db"
)

// The cap counts real databases on the server; at the limit Provision refuses
// without creating anything, and a limit of 0 means unlimited.
func TestTenantDatabaseManager_MaxDatabases_Postgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	const id = "limit_test_db"
	_ = db.DropTenantDatabase(ctx, dsn, id)
	t.Cleanup(func() { _ = db.DropTenantDatabase(ctx, dsn, id) })

	current, err := db.CountTenantDatabases(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	m := NewTenantDatabaseManager(dsn, nil)

	m.SetMaxDatabases(current) // already full
	if _, err := m.Provision(ctx, id); !errors.Is(err, ErrDedicatedDatabaseLimit) {
		t.Fatalf("at the limit: err = %v, want ErrDedicatedDatabaseLimit", err)
	}
	if n, _ := db.CountTenantDatabases(ctx, dsn); n != current {
		t.Fatalf("a refused provision created a database (%d -> %d)", current, n)
	}

	m.SetMaxDatabases(current + 1) // room for exactly one
	if _, err := m.Provision(ctx, id); err != nil {
		t.Fatalf("below the limit: %v", err)
	}

	m.SetMaxDatabases(0) // unlimited
	_ = db.DropTenantDatabase(ctx, dsn, id)
	if _, err := m.Provision(ctx, id); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
}
