package db

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTenantDatabaseName(t *testing.T) {
	ok := map[string]string{
		"acme":                  "recurso_acme",
		"  ACME  ":              "recurso_acme",
		"a1_b2":                 "recurso_a1_b2",
		"7eleven":               "recurso_7eleven",
		strings.Repeat("a", 40): "recurso_" + strings.Repeat("a", 40),
	}
	for in, want := range ok {
		got, err := TenantDatabaseName(in)
		if err != nil || got != want {
			t.Errorf("TenantDatabaseName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []string{
		"", "   ", "_x", "a b", "a-b", "a.b", "a;drop", `a"b`, "a'b", "ünï",
		"recurso", "a/../b", strings.Repeat("a", 41), "x\ny",
	}
	// "recurso" is a valid id (-> recurso_recurso); it is only listed above to
	// make the point that the prefix, not the id, decides the namespace.
	for _, in := range bad {
		if in == "recurso" {
			continue
		}
		if _, err := TenantDatabaseName(in); !errors.Is(err, ErrInvalidTenantDatabaseID) {
			t.Errorf("TenantDatabaseName(%q) err = %v, want ErrInvalidTenantDatabaseID", in, err)
		}
	}
}

func TestTenantDatabaseDSN(t *testing.T) {
	got, err := TenantDatabaseDSN("postgres://user:pw@db.internal:5432/recurso?sslmode=require", "recurso_acme")
	if err != nil {
		t.Fatal(err)
	}
	if want := "postgres://user:pw@db.internal:5432/recurso_acme?sslmode=require"; got != want {
		t.Errorf("DSN = %q, want %q", got, want)
	}
	if _, err := TenantDatabaseDSN("not a url", "recurso_x"); err == nil {
		t.Error("a non-URL admin DSN must be rejected")
	}
}

func TestProvisionTenantDatabase_Postgres(t *testing.T) {
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping postgres-backed provisioning test")
	}
	ctx := context.Background()
	const id = "provision_test_db"
	_ = DropTenantDatabase(ctx, adminDSN, id)
	t.Cleanup(func() { _ = DropTenantDatabase(ctx, adminDSN, id) })

	name, dsn, err := ProvisionTenantDatabase(ctx, adminDSN, id)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if name != "recurso_provision_test_db" {
		t.Errorf("name = %q", name)
	}

	// The new database is fully migrated and holds no workspace data. The only
	// row is the placeholder default tenant seeded by migration 000002, which
	// every database gets.
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var current string
	if err := conn.QueryRowContext(ctx, `SELECT current_database()`).Scan(&current); err != nil || current != name {
		t.Fatalf("connected to %q (err %v), want %q", current, err, name)
	}
	var tenants, invoices int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants WHERE id <> '00000000-0000-0000-0000-000000000001'`).Scan(&tenants); err != nil || tenants != 0 {
		t.Errorf("real tenants in new db = %d err %v, want 0", tenants, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM invoices`).Scan(&invoices); err != nil || invoices != 0 {
		t.Errorf("invoices = %d err %v; want an existing, empty table (migrations must have run)", invoices, err)
	}

	// Foreign keys into shared-only tables are gone, so business rows that name
	// a user or gateway connection can be written.
	var fkLeft int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_constraint c JOIN pg_class r ON r.oid=c.confrelid
		WHERE c.contype='f' AND r.relname IN ('users','gateway_connections','organizations')`).Scan(&fkLeft); err != nil || fkLeft != 0 {
		t.Errorf("foreign keys to shared-only tables = %d err %v, want 0", fkLeft, err)
	}

	// Provisioning the same id again must not touch the existing database.
	if _, _, err := ProvisionTenantDatabase(ctx, adminDSN, id); !errors.Is(err, ErrTenantDatabaseExists) {
		t.Errorf("second provision err = %v, want ErrTenantDatabaseExists", err)
	}

	// Invalid ids never reach SQL.
	if _, _, err := ProvisionTenantDatabase(ctx, adminDSN, `x"; DROP DATABASE recurso; --`); !errors.Is(err, ErrInvalidTenantDatabaseID) {
		t.Errorf("injection-shaped id err = %v, want ErrInvalidTenantDatabaseID", err)
	}

	if err := DropTenantDatabase(ctx, adminDSN, id); err != nil {
		t.Fatalf("drop: %v", err)
	}
	var left bool
	admin, _ := sql.Open("postgres", adminDSN)
	defer func() { _ = admin.Close() }()
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&left); err != nil || left {
		t.Errorf("database still exists after drop (err %v)", err)
	}
}

// TestProvisionFromTemplate_Postgres proves new databases are cloned from the
// migrated template and that provisioning still works when the template is gone.
func TestProvisionFromTemplate_Postgres(t *testing.T) {
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping postgres-backed template test")
	}
	ctx := context.Background()
	const withTpl, withoutTpl = "tpl_clone_test", "tpl_fallback_test"
	for _, id := range []string{withTpl, withoutTpl} {
		_ = DropTenantDatabase(ctx, adminDSN, id)
		id := id
		t.Cleanup(func() { _ = DropTenantDatabase(ctx, adminDSN, id) })
	}

	if err := EnsureTenantTemplate(ctx, adminDSN); err != nil {
		t.Fatalf("ensure template: %v", err)
	}
	if err := EnsureTenantTemplate(ctx, adminDSN); err != nil { // idempotent
		t.Fatalf("ensure template (2nd): %v", err)
	}

	start := time.Now()
	_, dsn, err := ProvisionTenantDatabase(ctx, adminDSN, withTpl)
	if err != nil {
		t.Fatalf("provision from template: %v", err)
	}
	t.Logf("clone provisioning took %s", time.Since(start))
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM invoices`).Scan(&n); err != nil || n != 0 {
		t.Errorf("cloned db invoices = %d err %v; want existing empty table", n, err)
	}

	// Template missing -> falls back to migrating an empty database.
	admin, _ := sql.Open("postgres", adminDSN)
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS "`+TenantTemplateName+`" WITH (FORCE)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = EnsureTenantTemplate(ctx, adminDSN) })
	_, dsn2, err := ProvisionTenantDatabase(ctx, adminDSN, withoutTpl)
	if err != nil {
		t.Fatalf("provision without template: %v", err)
	}
	c2, _ := sql.Open("postgres", dsn2)
	defer func() { _ = c2.Close() }()
	if err := c2.QueryRowContext(ctx, `SELECT COUNT(*) FROM invoices`).Scan(&n); err != nil {
		t.Errorf("fallback db not migrated: %v", err)
	}
}

// TestRoutedDB_OnFirstUse_Postgres: the hook runs once per database even under
// concurrent first requests, and a failing hook fails the query (fail closed)
// and is retried rather than leaving the database half-prepared.
func TestRoutedDB_OnFirstUse_Postgres(t *testing.T) {
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	const id = "first_use_test"
	_ = DropTenantDatabase(ctx, adminDSN, id)
	t.Cleanup(func() { _ = DropTenantDatabase(ctx, adminDSN, id) })
	name, _, err := ProvisionTenantDatabase(ctx, adminDSN, id)
	if err != nil {
		t.Fatal(err)
	}

	var calls int32
	var fail atomic.Bool
	fail.Store(true)
	var live atomic.Bool // the constructor pings; keep that on the shared database
	routed, err := NewRoutedDB(adminDSN, func(context.Context) (string, error) {
		if !live.Load() {
			return "", nil
		}
		return name, nil
	},
		RoutedDBOptions{MaxOpen: 8, OnFirstUse: func(context.Context, string, string) error {
			atomic.AddInt32(&calls, 1)
			if fail.Load() {
				return errors.New("boom")
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = routed.Close() }()

	live.Store(true)
	var n int
	if err := routed.QueryRowContext(ctx, `SELECT 1`).Scan(&n); err == nil {
		t.Fatal("query succeeded although the first-use hook failed")
	}
	fail.Store(false)
	atomic.StoreInt32(&calls, 0)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var db string
			if err := routed.QueryRowContext(ctx, `SELECT current_database()`).Scan(&db); err != nil || db != name {
				t.Errorf("db=%q err=%v want %q", db, err, name)
			}
		}()
	}
	wg.Wait()
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Errorf("OnFirstUse ran %d times, want 1", c)
	}
}

// Dropping a workspace database is irreversible, so it is refused unless the
// workspace is canceled and the exact name is typed.
func TestDropWorkspaceDatabase_Postgres(t *testing.T) {
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	shared, err := sql.Open("postgres", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	// Registered first so it runs last: the row-removal cleanups below still need
	// the connection (a plain defer would close it before they ran).
	t.Cleanup(func() { _ = shared.Close() })

	const id = "drop_guard_test"
	_, _ = shared.ExecContext(ctx, `DELETE FROM tenants WHERE database_name = $1`, TenantDatabasePrefix+id) // leftover from an aborted run
	_ = DropTenantDatabase(ctx, adminDSN, id)
	t.Cleanup(func() { _ = DropTenantDatabase(ctx, adminDSN, id) })
	name, _, err := ProvisionTenantDatabase(ctx, adminDSN, id)
	if err != nil {
		t.Fatal(err)
	}

	tenant := uuid.New()
	if _, err := shared.ExecContext(ctx,
		`INSERT INTO tenants (id, name, email, billing_status, database_name) VALUES ($1,'drop guard',$2,'active',$3)`,
		tenant, tenant.String()+"@example.com", name); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = shared.Exec(`DELETE FROM tenants WHERE id=$1`, tenant) })

	exists := func() bool {
		var ok bool
		_ = shared.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&ok)
		return ok
	}

	if _, err := DropWorkspaceDatabase(ctx, adminDSN, shared, tenant, name); !errors.Is(err, ErrWorkspaceStillActive) {
		t.Fatalf("active workspace: err = %v, want ErrWorkspaceStillActive", err)
	}
	if _, err := shared.ExecContext(ctx, `UPDATE tenants SET billing_status='canceled' WHERE id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := DropWorkspaceDatabase(ctx, adminDSN, shared, tenant, "recurso_wrong"); !errors.Is(err, ErrDropNotConfirmed) {
		t.Fatalf("wrong confirmation: err = %v, want ErrDropNotConfirmed", err)
	}
	if !exists() {
		t.Fatal("database was dropped despite a refused request")
	}
	if got, err := DropWorkspaceDatabase(ctx, adminDSN, shared, tenant, name); err != nil || got != name {
		t.Fatalf("drop: %q, %v", got, err)
	}
	if exists() {
		t.Error("database still exists after a confirmed drop")
	}

	other := uuid.New()
	if _, err := shared.ExecContext(ctx, `INSERT INTO tenants (id, name, email, billing_status) VALUES ($1,'shared one',$2,'canceled')`, other, other.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = shared.Exec(`DELETE FROM tenants WHERE id=$1`, other) })
	if _, err := DropWorkspaceDatabase(ctx, adminDSN, shared, other, "recurso"); !errors.Is(err, ErrWorkspaceNotDedicated) {
		t.Errorf("shared workspace: err = %v, want ErrWorkspaceNotDedicated", err)
	}
}

func TestCountTenantDatabases_Postgres(t *testing.T) {
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	const id = "count_test_db"
	_ = DropTenantDatabase(ctx, adminDSN, id)
	t.Cleanup(func() { _ = DropTenantDatabase(ctx, adminDSN, id) })

	before, err := CountTenantDatabases(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureTenantTemplate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountTenantDatabases(ctx, adminDSN); n != before {
		t.Errorf("the template must not be counted: %d -> %d", before, n)
	}
	if _, _, err := ProvisionTenantDatabase(ctx, adminDSN, id); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountTenantDatabases(ctx, adminDSN); n != before+1 {
		t.Errorf("count after provisioning = %d, want %d", n, before+1)
	}
}
