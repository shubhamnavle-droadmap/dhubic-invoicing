// Package workspace runs background work for every workspace.
//
// A workspace with a dedicated database keeps its business data out of the
// shared database, so a job that scans "all overdue invoices" or "all due
// subscriptions" would find only the shared workspaces' rows and silently skip
// the rest. Each runs the job's body once as before (shared database) and then
// once per dedicated workspace with that workspace on the context, which makes
// the routed database handle send the same queries to its database.
//
// With the feature off nothing is configured and Each is a plain call, so
// existing deployments behave exactly as before.
package workspace

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

// Lister returns the ids of the workspaces that have a dedicated database.
type Lister func(ctx context.Context) ([]uuid.UUID, error)

var lister atomic.Pointer[Lister]

// Configure enables per-workspace fan-out. Call once at startup; nil disables it.
func Configure(l Lister) {
	if l == nil {
		lister.Store(nil)
		return
	}
	lister.Store(&l)
}

// With returns ctx scoped to one workspace. Jobs that already loop over tenants
// use it so each tenant's queries reach that tenant's database.
func With(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, domain.TenantIDKey, tenantID)
}

// Each runs fn for the shared database first, exactly as the job ran before,
// then once for every dedicated workspace. A failure or panic in one workspace
// is logged and does not stop the others.
func Each(ctx context.Context, fn func(ctx context.Context)) {
	fn(ctx)

	l := lister.Load()
	if l == nil {
		return
	}
	ids, err := (*l)(ctx)
	if err != nil {
		slog.Error("workspace fan-out: could not list dedicated workspaces; they were skipped this run", "error", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		runOne(With(ctx, id), id, fn)
	}
}

func runOne(ctx context.Context, id uuid.UUID, fn func(ctx context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("workspace fan-out: job panicked for workspace", "tenant_id", id, "panic", r)
		}
	}()
	fn(ctx)
}

// Link adds ?w=<workspace> to a customer-facing link when ctx belongs to a
// dedicated workspace (as set by Each), so the public page it opens can find
// that workspace's database. On the shared pass ctx has no tenant and the link
// is returned unchanged.
func Link(ctx context.Context, link string) string {
	id, _ := ctx.Value(domain.TenantIDKey).(uuid.UUID)
	if id == uuid.Nil {
		return link
	}
	sep := "?"
	if strings.Contains(link, "?") {
		sep = "&"
	}
	return link + sep + "w=" + id.String()
}

var lockFactor atomic.Int64

// SetLockFactor sets how much longer a job's distributed lock may live when the
// job also visits dedicated workspaces. One lock covers the whole pass over every
// workspace, so the lease must outlast many small runs instead of one.
func SetLockFactor(f int) {
	if f < 1 {
		f = 1
	}
	lockFactor.Store(int64(f))
}

// LockTTL scales a job's lock lease by the configured factor (1 = unchanged,
// which is what an install without dedicated databases always gets).
func LockTTL(base time.Duration) time.Duration {
	if f := lockFactor.Load(); f > 1 {
		return base * time.Duration(f)
	}
	return base
}
