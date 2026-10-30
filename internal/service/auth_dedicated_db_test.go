package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

type fakeDedicatedDBs struct {
	provisioned []string
	assigned    map[uuid.UUID]string
	dropped     []string
	provisionErr,
	assignErr error
}

func (f *fakeDedicatedDBs) Provision(_ context.Context, id string) (string, error) {
	if f.provisionErr != nil {
		return "", f.provisionErr
	}
	f.provisioned = append(f.provisioned, id)
	return "recurso_" + id, nil
}

func (f *fakeDedicatedDBs) Assign(_ context.Context, tenant *domain.Tenant, name string) error {
	if f.assignErr != nil {
		return f.assignErr
	}
	if f.assigned == nil {
		f.assigned = map[uuid.UUID]string{}
	}
	f.assigned[tenant.ID] = name
	return nil
}

func (f *fakeDedicatedDBs) Drop(_ context.Context, id string) error {
	f.dropped = append(f.dropped, id)
	return nil
}

func TestRegisterWithDatabase_AssignsDedicatedDatabase(t *testing.T) {
	svc, _, _ := newTestAuth()
	fake := &fakeDedicatedDBs{}
	svc.ConfigureDedicatedDatabases(fake)

	res, err := svc.RegisterWithDatabase(context.Background(), "Acme", "Alice", "a@acme.com", "supersecret", "ua", "", " acme ")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(fake.provisioned) != 1 || fake.provisioned[0] != "acme" {
		t.Errorf("provisioned = %v, want [acme] (trimmed)", fake.provisioned)
	}
	if got := fake.assigned[res.Tenant.ID]; got != "recurso_acme" {
		t.Errorf("assigned = %q, want recurso_acme on the new tenant", got)
	}
	if res.Tenant.DatabaseName != "recurso_acme" {
		t.Errorf("tenant.DatabaseName = %q, want recurso_acme", res.Tenant.DatabaseName)
	}
	if len(fake.dropped) != 0 {
		t.Errorf("a successful signup must not drop the database (dropped %v)", fake.dropped)
	}
}

// Signups that do not ask for a dedicated database are unchanged, whether or
// not the feature is configured.
func TestRegisterWithDatabase_NoIDUsesSharedDatabase(t *testing.T) {
	svc, _, _ := newTestAuth()
	fake := &fakeDedicatedDBs{}
	svc.ConfigureDedicatedDatabases(fake)

	res, err := svc.RegisterWithDatabase(context.Background(), "Acme", "Alice", "a@acme.com", "supersecret", "ua", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(fake.provisioned) != 0 || res.Tenant.DatabaseName != "" {
		t.Errorf("provisioned %v, database %q; want none (shared database)", fake.provisioned, res.Tenant.DatabaseName)
	}
}

func TestRegisterWithDatabase_RejectedWhenFeatureOff(t *testing.T) {
	svc, ur, _ := newTestAuth() // ConfigureDedicatedDatabases never called
	_, err := svc.RegisterWithDatabase(context.Background(), "Acme", "Alice", "a@acme.com", "supersecret", "ua", "", "acme")
	if !errors.Is(err, ErrDedicatedDatabaseDisabled) {
		t.Fatalf("err = %v, want ErrDedicatedDatabaseDisabled", err)
	}
	if len(ur.users) != 0 {
		t.Errorf("a rejected signup must not create a user (users = %d)", len(ur.users))
	}
}

func TestRegisterWithDatabase_ProvisionFailureCreatesNothing(t *testing.T) {
	svc, ur, _ := newTestAuth()
	fake := &fakeDedicatedDBs{provisionErr: ErrDatabaseExists}
	svc.ConfigureDedicatedDatabases(fake)

	_, err := svc.RegisterWithDatabase(context.Background(), "Acme", "Alice", "a@acme.com", "supersecret", "ua", "", "acme")
	if !errors.Is(err, ErrDatabaseExists) {
		t.Fatalf("err = %v, want ErrDatabaseExists", err)
	}
	if len(ur.users) != 0 {
		t.Errorf("users = %d, want 0", len(ur.users))
	}
	if len(fake.dropped) != 0 {
		t.Errorf("must not drop a database it did not create (dropped %v)", fake.dropped)
	}
}

func TestRegisterWithDatabase_AssignFailureDropsNewDatabase(t *testing.T) {
	svc, ur, _ := newTestAuth()
	fake := &fakeDedicatedDBs{assignErr: errors.New("db down")}
	svc.ConfigureDedicatedDatabases(fake)

	if _, err := svc.RegisterWithDatabase(context.Background(), "Acme", "Alice", "a@acme.com", "supersecret", "ua", "", "acme"); err == nil {
		t.Fatal("want an error when the database cannot be assigned")
	}
	if len(fake.dropped) != 1 || fake.dropped[0] != "acme" {
		t.Errorf("dropped = %v, want [acme] so a retry starts clean", fake.dropped)
	}
	if len(ur.users) != 0 {
		t.Errorf("users = %d, want 0", len(ur.users))
	}
}

// At the database limit the signup is refused before anything is created.
func TestRegisterWithDatabase_LimitReachedCreatesNothing(t *testing.T) {
	svc, ur, _ := newTestAuth()
	fake := &fakeDedicatedDBs{provisionErr: ErrDedicatedDatabaseLimit}
	svc.ConfigureDedicatedDatabases(fake)

	_, err := svc.RegisterWithDatabase(context.Background(), "Acme", "Alice", "a@acme.com", "supersecret", "ua", "", "acme")
	if !errors.Is(err, ErrDedicatedDatabaseLimit) {
		t.Fatalf("err = %v, want ErrDedicatedDatabaseLimit", err)
	}
	if len(ur.users) != 0 {
		t.Errorf("a refused signup must not create a user (users = %d)", len(ur.users))
	}
}
