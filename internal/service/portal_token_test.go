package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/adapter/db"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

// A workspace with a dedicated database gets a session token that names it, so
// the portal cookie alone is enough to find the session row; every other token is
// untouched and reads back as "no workspace".
func TestPortalSessionToken_Workspace(t *testing.T) {
	raw := db.GenerateSecureToken()

	if got := workspaceSessionToken(context.Background(), raw); got != raw {
		t.Errorf("no workspace: token must be unchanged, got %q", got)
	}
	if _, ok := TenantFromSessionToken(raw); ok {
		t.Error("an ordinary token must not name a workspace")
	}

	id := uuid.New()
	tok := workspaceSessionToken(context.WithValue(context.Background(), domain.TenantIDKey, id), raw)
	if !strings.HasSuffix(tok, "."+raw) {
		t.Errorf("the random part must survive intact, got %q", tok)
	}
	got, ok := TenantFromSessionToken(tok)
	if !ok || got != id {
		t.Errorf("round trip = %v, %v; want %v", got, ok, id)
	}

	for _, bad := range []string{"", ".", "nope.abc", uuid.Nil.String() + ".abc", "a.b.c"} {
		if _, ok := TenantFromSessionToken(bad); ok {
			t.Errorf("%q must not name a workspace", bad)
		}
	}
}
