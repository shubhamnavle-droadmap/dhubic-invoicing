package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

var (
	// ErrWorkspaceNotDedicated means the tenant has no dedicated database to drop.
	ErrWorkspaceNotDedicated = errors.New("workspace has no dedicated database")
	// ErrWorkspaceStillActive means the tenant is not canceled, so its data is live.
	ErrWorkspaceStillActive = errors.New("workspace is not canceled; refusing to drop its database")
	// ErrDropNotConfirmed means the confirmation did not match the database name.
	ErrDropNotConfirmed = errors.New("confirmation does not match the database name")
)

// DropWorkspaceDatabase permanently deletes a canceled workspace's dedicated
// database. Dropping a database cannot be undone, so it is deliberately hard to
// do by accident:
//
//   - the workspace must already be canceled (billing_status = canceled);
//   - confirm must be the exact database name (recurso_<id>), typed by the caller;
//   - only recurso_-prefixed names are ever dropped (DropTenantDatabase enforces it).
//
// The tenant row keeps its database_name, so a later request for that workspace
// fails closed instead of silently falling back to the shared database. Take a
// backup first (docs/dedicated-databases/07-BACKUP-AND-DELETE-RUNBOOK.md).
func DropWorkspaceDatabase(ctx context.Context, adminDSN string, shared *sql.DB, tenantID uuid.UUID, confirm string) (string, error) {
	var name sql.NullString
	var status string
	if err := shared.QueryRowContext(ctx,
		`SELECT database_name, billing_status FROM tenants WHERE id = $1`, tenantID).Scan(&name, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("tenant %s not found", tenantID)
		}
		return "", err
	}
	if !name.Valid || name.String == "" {
		return "", ErrWorkspaceNotDedicated
	}
	if status != domain.BillingStatusCanceled {
		return "", fmt.Errorf("%w (status %q)", ErrWorkspaceStillActive, status)
	}
	if strings.TrimSpace(confirm) != name.String {
		return "", fmt.Errorf("%w: type %s to confirm", ErrDropNotConfirmed, name.String)
	}
	id := strings.TrimPrefix(name.String, TenantDatabasePrefix)
	if err := DropTenantDatabase(ctx, adminDSN, id); err != nil {
		return "", err
	}
	return name.String, nil
}
