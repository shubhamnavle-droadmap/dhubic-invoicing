package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

func TestWorkspaceMismatch(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	withA := context.WithValue(context.Background(), domain.TenantIDKey, a)
	if workspaceMismatch(withA, a) {
		t.Error("same workspace must not mismatch")
	}
	if !workspaceMismatch(withA, b) {
		t.Error("a link for workspace A must not open workspace B's record")
	}
	if workspaceMismatch(context.Background(), b) {
		t.Error("links without a workspace marker keep working")
	}
}
