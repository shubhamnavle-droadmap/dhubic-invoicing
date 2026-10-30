package handler

import (
	"context"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

// workspaceMismatch reports whether the request named a workspace (the ?w=
// link parameter, put on the context by middleware.WorkspaceFromQuery) and the
// record being served belongs to a different one. Public endpoints look records
// up by id alone, so without this a link for workspace A could open workspace
// B's invoice whenever both live in the same database. Requests that name no
// workspace (all links issued before dedicated databases existed) are unaffected.
func workspaceMismatch(ctx context.Context, recordTenant uuid.UUID) bool {
	t, _ := ctx.Value(domain.TenantIDKey).(uuid.UUID)
	return t != uuid.Nil && t != recordTenant
}
