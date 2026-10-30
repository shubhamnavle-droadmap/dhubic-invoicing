package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/adapter/db"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

var (
	// ErrDedicatedDatabaseDisabled: a signup asked for its own database but this
	// deployment has not enabled the feature (DEDICATED_DATABASES_ENABLED).
	ErrDedicatedDatabaseDisabled = errors.New("dedicated databases are not enabled on this deployment")
	// ErrDedicatedDatabaseLimit: the deployment already has the maximum number of
	// dedicated databases it allows (DEDICATED_DATABASES_MAX).
	ErrDedicatedDatabaseLimit = errors.New("the maximum number of dedicated databases has been reached")
	// ErrInvalidDatabaseID: the requested id cannot form a safe database name.
	ErrInvalidDatabaseID = db.ErrInvalidTenantDatabaseID
	// ErrDatabaseExists: a database with that name already exists.
	ErrDatabaseExists = db.ErrTenantDatabaseExists
)

// dedicatedDatabases is what AuthService needs to give a new workspace its own
// database. *TenantDatabaseManager satisfies it; tests supply a fake.
type dedicatedDatabases interface {
	// Provision creates recurso_<id>, migrates it and returns its name.
	Provision(ctx context.Context, id string) (name string, err error)
	// Assign seeds the tenant into database name and records it as the tenant's
	// database; after it returns, requests for the tenant are routed there.
	Assign(ctx context.Context, tenant *domain.Tenant, name string) error
	// Drop removes a database created by Provision (rollback on a failed signup).
	Drop(ctx context.Context, id string) error
}

// TenantDatabaseManager creates and assigns dedicated per-workspace databases.
type TenantDatabaseManager struct {
	adminDSN string
	tenants  *db.TenantRepository
	onAssign func(tenantID uuid.UUID) // e.g. TenantRouter.Forget; may be nil
	max      int                       // 0 = unlimited
}

// NewTenantDatabaseManager takes the DSN of a role allowed to CREATE DATABASE
// (normally the application's own DATABASE_URL).
func NewTenantDatabaseManager(adminDSN string, tenants *db.TenantRepository) *TenantDatabaseManager {
	return &TenantDatabaseManager{adminDSN: adminDSN, tenants: tenants}
}

// OnAssign registers a callback run after a tenant is assigned a database (used
// to drop that tenant's cached route).
func (m *TenantDatabaseManager) OnAssign(fn func(tenantID uuid.UUID)) { m.onAssign = fn }

// SetMaxDatabases caps how many dedicated databases may exist (0 = unlimited).
// Each one costs disk, connections, migrations and backup time, so a bug or abuse
// upstream of the token check must not be able to create them without bound.
// The check is against Postgres itself and is not atomic across replicas: two
// simultaneous signups at the limit can overshoot it by a few.
func (m *TenantDatabaseManager) SetMaxDatabases(n int) { m.max = n }

func (m *TenantDatabaseManager) Provision(ctx context.Context, id string) (string, error) {
	if m.max > 0 {
		n, err := db.CountTenantDatabases(ctx, m.adminDSN)
		if err != nil {
			return "", err
		}
		if n >= m.max {
			return "", ErrDedicatedDatabaseLimit
		}
	}
	name, _, err := db.ProvisionTenantDatabase(ctx, m.adminDSN, id)
	return name, err
}

// Assign seeds the tenant row into the new database FIRST and only then records
// the assignment, so routing can never point at a database that cannot accept
// the tenant's data.
func (m *TenantDatabaseManager) Assign(ctx context.Context, tenant *domain.Tenant, name string) error {
	dsn, err := db.TenantDatabaseDSN(m.adminDSN, name)
	if err != nil {
		return err
	}
	if err := db.SeedTenantInDatabase(ctx, dsn, tenant); err != nil {
		return err
	}
	if err := m.tenants.SetDatabaseName(ctx, tenant.ID, name); err != nil {
		return err
	}
	if m.onAssign != nil {
		m.onAssign(tenant.ID)
	}
	return nil
}

func (m *TenantDatabaseManager) Drop(ctx context.Context, id string) error {
	return db.DropTenantDatabase(ctx, m.adminDSN, id)
}
