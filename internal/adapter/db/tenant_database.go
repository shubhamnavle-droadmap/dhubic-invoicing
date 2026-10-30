package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

// TenantDatabasePrefix is prepended to the operator-supplied id to form a
// dedicated workspace database name: id "acme" -> database "recurso_acme".
const TenantDatabasePrefix = "recurso_"

// TenantTemplateName is the migrated, empty database new workspaces are cloned
// from. The double underscore cannot be produced by TenantDatabaseName (ids
// start with a letter or digit), so no workspace can ever collide with it.
const TenantTemplateName = TenantDatabasePrefix + "_template"

var (
	// ErrInvalidTenantDatabaseID means the id cannot be turned into a safe name.
	ErrInvalidTenantDatabaseID = errors.New("invalid database id")
	// ErrTenantDatabaseExists means a database with that name already exists.
	ErrTenantDatabaseExists = errors.New("tenant database already exists")

	// The id becomes part of a SQL identifier, which cannot be a bind
	// parameter, so it is restricted to a conservative alphabet instead.
	tenantDatabaseID = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,39}$`)
)

// TenantDatabaseName returns the dedicated database name for an operator id.
// The id is lower-cased and trimmed; anything outside [a-z0-9_] (after the
// first character) is rejected rather than rewritten, so two different ids can
// never collapse onto the same database.
func TenantDatabaseName(id string) (string, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !tenantDatabaseID.MatchString(id) {
		return "", fmt.Errorf("%w: use 1-40 characters from a-z, 0-9 and _, starting with a letter or digit", ErrInvalidTenantDatabaseID)
	}
	return TenantDatabasePrefix + id, nil
}

// TenantDatabaseDSN returns adminDSN pointed at the named database: same host,
// credentials and options, different database.
func TenantDatabaseDSN(adminDSN, dbName string) (string, error) {
	u, err := url.Parse(adminDSN)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("admin DSN is not a postgres:// URL")
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

// ProvisionTenantDatabase creates the dedicated database for id, applies every
// migration to it and returns its name and DSN.
//
// adminDSN must belong to a role allowed to CREATE DATABASE. If creation or
// migration fails after the database was created, the new database is dropped
// again so a retry starts from nothing instead of finding a half-built schema.
func ProvisionTenantDatabase(ctx context.Context, adminDSN, id string) (name, dsn string, err error) {
	name, err = TenantDatabaseName(id)
	if err != nil {
		return "", "", err
	}
	dsn, err = TenantDatabaseDSN(adminDSN, name)
	if err != nil {
		return "", "", err
	}

	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return "", "", fmt.Errorf("open admin connection: %w", err)
	}
	defer func() { _ = admin.Close() }()

	var exists bool
	if err := admin.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", "", fmt.Errorf("check database: %w", err)
	}
	if exists {
		return "", "", fmt.Errorf("%w: %s", ErrTenantDatabaseExists, name)
	}

	if err := createFromTemplate(ctx, admin, name); err != nil {
		if errors.Is(err, ErrTenantDatabaseExists) {
			return "", "", fmt.Errorf("%w: %s", err, name)
		}
		return "", "", err
	}

	// Cheap no-op when the clone is current; brings a stale template up to date.
	if err := PrepareTenantDatabase(ctx, dsn); err != nil {
		_ = DropTenantDatabase(ctx, adminDSN, id)
		return "", "", fmt.Errorf("migrate %s: %w", name, err)
	}
	return name, dsn, nil
}

// DropTenantDatabase removes a dedicated database. It only ever accepts an id
// that maps to a recurso_-prefixed name, so it cannot be pointed at the shared
// database or at anything outside this feature.
func DropTenantDatabase(ctx context.Context, adminDSN, id string) error {
	name, err := TenantDatabaseName(id)
	if err != nil {
		return err
	}
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("open admin connection: %w", err)
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+pq.QuoteIdentifier(name)+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

// SeedTenantInDatabase copies the workspace's tenant row into its dedicated
// database. About fifty tables reference tenants(id), so nothing can be written
// there until the row exists. The tenants-insert trigger then creates the
// primary entity and its invoice sequence, exactly as in the shared database.
// It is idempotent.
func SeedTenantInDatabase(ctx context.Context, dsn string, t *domain.Tenant) error {
	d, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open tenant database: %w", err)
	}
	defer func() { _ = d.Close() }()

	status, tier := t.BillingStatus, t.PlanTier
	if status == "" {
		status = domain.BillingStatusActive
	}
	if tier == "" {
		tier = domain.PlanTierFree
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO tenants (id, name, email, created_at, updated_at, trial_ends_at, billing_status, plan_tier)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`,
		t.ID, t.Name, t.Email, t.CreatedAt, t.UpdatedAt, t.TrialEndsAt, status, tier); err != nil {
		return fmt.Errorf("seed tenant row: %w", err)
	}
	return nil
}

// createFromTemplate creates name by cloning the template database (a file-level
// copy, ~0.3 s) and falls back to an empty database that the caller migrates
// (~5 s) when the template is missing or stays busy.
func createFromTemplate(ctx context.Context, admin *sql.DB, name string) error {
	quoted := pq.QuoteIdentifier(name)
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		_, err = admin.ExecContext(ctx, "CREATE DATABASE "+quoted+" TEMPLATE "+pq.QuoteIdentifier(TenantTemplateName))
		var pqErr *pq.Error
		if err == nil {
			return nil
		}
		if !errors.As(err, &pqErr) {
			return fmt.Errorf("create database: %w", err)
		}
		switch pqErr.Code {
		case "42P04": // duplicate_database: lost a race
			return ErrTenantDatabaseExists
		case "55006": // object_in_use: someone is migrating the template; retry shortly
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			continue
		case "3D000": // template does not exist yet
		default:
			return fmt.Errorf("create database: %w", err)
		}
		break
	}
	// Fallback: empty database, migrated by the caller.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+quoted); err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "42P04" {
			return ErrTenantDatabaseExists
		}
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

// EnsureTenantTemplate creates the template database if needed and brings it to
// the current schema. Call it at startup, after the shared database is migrated.
// Safe to run from several replicas at once: creation tolerates a lost race and
// migrations take golang-migrate's advisory lock.
func EnsureTenantTemplate(ctx context.Context, adminDSN string) error {
	dsn, err := TenantDatabaseDSN(adminDSN, TenantTemplateName)
	if err != nil {
		return err
	}
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("open admin connection: %w", err)
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(TenantTemplateName)); err != nil {
		var pqErr *pq.Error
		if !errors.As(err, &pqErr) || pqErr.Code != "42P04" {
			return fmt.Errorf("create template database: %w", err)
		}
	}
	if err := RunMigrations(dsn); err != nil {
		return fmt.Errorf("migrate template database: %w", err)
	}
	return nil
}

// sharedOnlyTables are control-plane tables that live only in the shared
// database. A dedicated database has the same schema, so it also has (empty)
// copies of them, and foreign keys from business tables into them could never be
// satisfied there: a credit note's created_by user or a customer's gateway
// connection exists only in the shared database.
var sharedOnlyTables = []string{"users", "gateway_connections", "organizations"}

// PrepareTenantDatabase brings a dedicated database to the current schema and
// drops the foreign keys that point at shared-only tables. It is idempotent and
// runs both when a database is provisioned and on first use after each release,
// so constraints added by new migrations are handled too.
func PrepareTenantDatabase(ctx context.Context, dsn string) error {
	if err := RunMigrations(dsn); err != nil {
		return err
	}
	d, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open tenant database: %w", err)
	}
	defer func() { _ = d.Close() }()

	rows, err := d.QueryContext(ctx, `
		SELECT c.conrelid::regclass::text, c.conname
		FROM pg_constraint c
		JOIN pg_class ref ON ref.oid = c.confrelid
		WHERE c.contype = 'f'
		  AND ref.relnamespace = 'public'::regnamespace
		  AND ref.relname = ANY($1)`, pq.Array(sharedOnlyTables))
	if err != nil {
		return fmt.Errorf("list shared-only foreign keys: %w", err)
	}
	type fk struct{ table, name string }
	var fks []fk
	for rows.Next() {
		var f fk
		if err := rows.Scan(&f.table, &f.name); err != nil {
			_ = rows.Close()
			return err
		}
		fks = append(fks, f)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, f := range fks {
		// f.table comes from regclass (already quoted where needed); the
		// constraint name is quoted here.
		if _, err := d.ExecContext(ctx, "ALTER TABLE "+f.table+" DROP CONSTRAINT IF EXISTS "+pq.QuoteIdentifier(f.name)); err != nil {
			return fmt.Errorf("drop %s.%s: %w", f.table, f.name, err)
		}
	}
	return nil
}

// CountTenantDatabases returns how many dedicated workspace databases exist on
// the server (the template is not counted). It reads Postgres itself, so it also
// sees databases created by other replicas and ones left behind by a failed
// signup.
func CountTenantDatabases(ctx context.Context, adminDSN string) (int, error) {
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return 0, fmt.Errorf("open admin connection: %w", err)
	}
	defer func() { _ = admin.Close() }()
	var n int
	err = admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pg_database WHERE datname LIKE $1 AND datname <> $2`,
		`recurso\_%`, TenantTemplateName).Scan(&n)
	return n, err
}
