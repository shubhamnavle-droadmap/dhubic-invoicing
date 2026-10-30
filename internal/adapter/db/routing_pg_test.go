package db

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

// TestRoutedDB_Postgres proves the routing layer against real Postgres: a shared
// database plus two dedicated ones, a single shared connection pool, and many
// concurrent callers for different workspaces. Every query must land in the
// database of the tenant on its context.
func TestRoutedDB_Postgres(t *testing.T) {
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping postgres-backed routing test")
	}
	ctx := context.Background()

	ids := []string{"rt_shared", "rt_a", "rt_b"}
	for _, id := range ids {
		_ = DropTenantDatabase(ctx, adminDSN, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_ = DropTenantDatabase(ctx, adminDSN, id)
		}
	})
	dsns := map[string]string{}
	for _, id := range ids {
		name, dsn, err := ProvisionTenantDatabase(ctx, adminDSN, id)
		if err != nil {
			t.Fatalf("provision %s: %v", id, err)
		}
		dsns[name] = dsn
	}
	const sharedName, aName, bName = "recurso_rt_shared", "recurso_rt_a", "recurso_rt_b"

	open := func(dsn string) *sql.DB {
		d, err := sql.Open("postgres", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	}
	sharedDirect, aDirect, bDirect := open(dsns[sharedName]), open(dsns[aName]), open(dsns[bName])

	// Three workspaces registered in the shared database; A and B have their own
	// database (and are seeded into it), S stays on the shared one.
	mk := func(label string, dedicated string) *domain.Tenant {
		now := time.Now().UTC()
		tn := &domain.Tenant{ID: uuid.New(), Name: label, Email: label + "@t.invalid", CreatedAt: now, UpdatedAt: now}
		if _, err := sharedDirect.ExecContext(ctx,
			`INSERT INTO tenants (id, name, email, created_at, updated_at, billing_status, plan_tier, database_name)
			 VALUES ($1,$2,$3,$4,$5,'active','free', NULLIF($6,''))`,
			tn.ID, tn.Name, tn.Email, tn.CreatedAt, tn.UpdatedAt, dedicated); err != nil {
			t.Fatalf("insert tenant %s: %v", label, err)
		}
		if dedicated != "" {
			if err := SeedTenantInDatabase(ctx, dsns[dedicated], tn); err != nil {
				t.Fatalf("seed %s: %v", label, err)
			}
		}
		return tn
	}
	tA, tB, tS := mk("route-a", aName), mk("route-b", bName), mk("route-s", "")
	want := map[uuid.UUID]string{tA.ID: aName, tB.ID: bName, tS.ID: sharedName}

	router := NewTenantRouter(sharedDirect, 2*time.Second)
	routed, err := NewRoutedDB(dsns[sharedName], router.Route)
	if err != nil {
		t.Fatalf("NewRoutedDB: %v", err)
	}
	t.Cleanup(func() { _ = routed.Close() })
	tctx := func(id uuid.UUID) context.Context { return context.WithValue(ctx, domain.TenantIDKey, id) }

	current := func(c context.Context) (string, error) {
		var n string
		err := routed.QueryRowContext(c, `SELECT current_database()`).Scan(&n)
		return n, err
	}

	// 1. Each tenant reaches its own database; no tenant on the context = shared.
	for id, db := range want {
		if got, err := current(tctx(id)); err != nil || got != db {
			t.Errorf("tenant %s routed to %q (err %v), want %q", id, got, err, db)
		}
	}
	if got, err := current(ctx); err != nil || got != sharedName {
		t.Errorf("no tenant on ctx routed to %q (err %v), want shared", got, err)
	}

	// 2. Writes land only in the owner's database. The primary-entity trigger
	// fired when each tenant was seeded, so entities exist where they should.
	insertCustomer := func(c context.Context, tenant uuid.UUID, email string) {
		t.Helper()
		if _, err := routed.ExecContext(c,
			`INSERT INTO customers (id, tenant_id, email, name, country, tax_type, ledger_account_id, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,' ','individual',$5,NOW(),NOW())`,
			uuid.New(), tenant, email, email, uuid.New()); err != nil {
			t.Fatalf("insert customer %s: %v", email, err)
		}
	}
	insertCustomer(tctx(tA.ID), tA.ID, "only-in-a@x.invalid")
	insertCustomer(tctx(tB.ID), tB.ID, "only-in-b@x.invalid")
	insertCustomer(tctx(tS.ID), tS.ID, "only-in-shared@x.invalid")
	count := func(d *sql.DB, email string) int {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM customers WHERE email=$1`, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for email, home := range map[string]*sql.DB{"only-in-a@x.invalid": aDirect, "only-in-b@x.invalid": bDirect, "only-in-shared@x.invalid": sharedDirect} {
		for name, d := range map[string]*sql.DB{"a": aDirect, "b": bDirect, "shared": sharedDirect} {
			want := 0
			if d == home {
				want = 1
			}
			if got := count(d, email); got != want {
				t.Errorf("%s found %d time(s) in %s, want %d", email, got, name, want)
			}
		}
	}
	var entities int
	if err := aDirect.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE tenant_id=$1 AND is_primary`, tA.ID).Scan(&entities); err != nil || entities != 1 {
		t.Errorf("dedicated database A has %d primary entities for its tenant (err %v), want 1 from the trigger", entities, err)
	}

	// 3. Transactions stay on one database for their whole life.
	for id, db := range want {
		tx, err := routed.BeginTx(tctx(id), nil)
		if err != nil {
			t.Fatal(err)
		}
		var first, second string
		_ = tx.QueryRowContext(tctx(id), `SELECT current_database()`).Scan(&first)
		_ = tx.QueryRowContext(tctx(id), `SELECT current_database()`).Scan(&second)
		_ = tx.Commit()
		if first != db || second != db {
			t.Errorf("tx for %s saw %q then %q, want %q throughout", id, first, second, db)
		}
	}

	// 4. The pool is shared (max 25 conns) and 60 goroutines interleave three
	// tenants. A pooled connection must never serve the wrong tenant.
	ids3 := []uuid.UUID{tA.ID, tB.ID, tS.ID}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := 0; g < 60; g++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 40; i++ {
				id := ids3[rng.Intn(len(ids3))]
				got, err := current(tctx(id))
				if err != nil {
					errs <- fmt.Sprintf("query for %s failed: %v", id, err)
					return
				}
				if got != want[id] {
					errs <- fmt.Sprintf("tenant %s served by %q, want %q", id, got, want[id])
					return
				}
			}
		}(int64(g))
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	// 5. Fail closed. A tenant whose database is missing, tampered with, or whose
	// route cannot be read must get an error, never the shared database.
	broken := map[string]string{
		"missing database": "recurso_does_not_exist",
		"not a tenant db":  "postgres",
		"injection":        `recurso_x"; DROP DATABASE recurso_rt_shared; --`,
	}
	for label, dbName := range broken {
		bt := mk("route-broken-"+uuid.NewString()[:6], "")
		if _, err := sharedDirect.ExecContext(ctx, `UPDATE tenants SET database_name=$1 WHERE id=$2`, dbName, bt.ID); err != nil {
			t.Fatal(err)
		}
		router.Forget(bt.ID)
		if got, err := current(tctx(bt.ID)); err == nil {
			t.Errorf("%s: query succeeded on %q, want an error (fail closed)", label, got)
		}
	}
	deadShared := open(dsns[sharedName])
	_ = deadShared.Close()
	deadRouter := NewTenantRouter(deadShared, time.Second)
	deadRouted, err := NewRoutedDB(dsns[sharedName], deadRouter.Route)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = deadRouted.Close() }()
	var n string
	if err := deadRouted.QueryRowContext(tctx(uuid.New()), `SELECT current_database()`).Scan(&n); err == nil {
		t.Errorf("route lookup failed yet the query ran on %q, want an error", n)
	}
}
