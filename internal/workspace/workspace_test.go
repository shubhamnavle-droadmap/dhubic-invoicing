package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

func tenantOf(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(domain.TenantIDKey).(uuid.UUID)
	return id
}

func TestEach_NotConfigured_IsAPlainCall(t *testing.T) {
	Configure(nil)
	calls := 0
	Each(context.Background(), func(ctx context.Context) {
		calls++
		if tenantOf(ctx) != uuid.Nil {
			t.Error("shared run must carry no tenant")
		}
	})
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestEach_RunsSharedThenEachWorkspace(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	Configure(func(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{a, b}, nil })
	defer Configure(nil)

	var seen []uuid.UUID
	Each(context.Background(), func(ctx context.Context) { seen = append(seen, tenantOf(ctx)) })
	if len(seen) != 3 || seen[0] != uuid.Nil || seen[1] != a || seen[2] != b {
		t.Errorf("order = %v, want [nil a b]", seen)
	}
}

func TestEach_OneWorkspacePanicDoesNotStopTheRest(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	Configure(func(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{a, b}, nil })
	defer Configure(nil)

	var ran []uuid.UUID
	Each(context.Background(), func(ctx context.Context) {
		if tenantOf(ctx) == a {
			panic("boom")
		}
		ran = append(ran, tenantOf(ctx))
	})
	if len(ran) != 2 || ran[1] != b {
		t.Errorf("ran = %v, want shared and b after a panicked", ran)
	}
}

func TestEach_ListFailureStillRunsShared(t *testing.T) {
	Configure(func(context.Context) ([]uuid.UUID, error) { return nil, errors.New("db down") })
	defer Configure(nil)
	calls := 0
	Each(context.Background(), func(context.Context) { calls++ })
	if calls != 1 {
		t.Errorf("calls = %d, want shared run only", calls)
	}
}

func TestEach_StopsWhenContextCancelled(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	Configure(func(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{a, b}, nil })
	defer Configure(nil)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	Each(ctx, func(context.Context) { calls++; cancel() })
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (cancelled after the shared run)", calls)
	}
}

func TestLink(t *testing.T) {
	id := uuid.New()
	if got := Link(context.Background(), "https://a/b"); got != "https://a/b" {
		t.Errorf("shared pass must leave the link alone, got %q", got)
	}
	if got := Link(With(context.Background(), id), "https://a/b"); got != "https://a/b?w="+id.String() {
		t.Errorf("got %q", got)
	}
	if got := Link(With(context.Background(), id), "https://a/b?x=1"); got != "https://a/b?x=1&w="+id.String() {
		t.Errorf("got %q", got)
	}
}

func TestLockTTL(t *testing.T) {
	defer SetLockFactor(1)
	SetLockFactor(1)
	if got := LockTTL(10 * time.Minute); got != 10*time.Minute {
		t.Errorf("default must be unchanged, got %v", got)
	}
	SetLockFactor(3)
	if got := LockTTL(10 * time.Minute); got != 30*time.Minute {
		t.Errorf("factor 3 = %v, want 30m", got)
	}
	SetLockFactor(0)
	if got := LockTTL(10 * time.Minute); got != 10*time.Minute {
		t.Errorf("factor below 1 must clamp to 1, got %v", got)
	}
}
