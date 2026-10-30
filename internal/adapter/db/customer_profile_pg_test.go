package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

func profileCustomer(tenantID uuid.UUID, email string) *domain.Customer {
	name := "Profile Test"
	return &domain.Customer{
		ID:              uuid.New(),
		TenantID:        tenantID,
		Email:           email,
		Name:            &name,
		LedgerAccountID: uuid.New(),
		TaxType:         "business",
		Active:          true,
		CreatedAt:       time.Now().UTC(),
	}
}

func TestCustomerProfileFields_Postgres(t *testing.T) {
	conn := openCustomerPMTestDB(t)
	repo := NewCustomerRepository(conn)
	ctx := context.Background()

	tenantA, _ := seedPMTenantCustomer(t, conn.DB)
	tenantB, _ := seedPMTenantCustomer(t, conn.DB)
	ctxA := context.WithValue(ctx, domain.TenantIDKey, tenantA)

	ext := "CUST-00042"
	c := profileCustomer(tenantA, "profile-a@example.com")
	c.ExternalID = &ext
	c.LegalName = "Acme Corporation Inc."
	c.Locale = "en-GB"
	c.PaymentTerms = "NET_30"
	c.Website = "https://acme.example"
	c.Notes = "Prefers invoices on the 1st."
	c.Tags = []string{"priority", "annual-contract"}
	c.BillingEmails = []string{"ap@acme.example", "finance@acme.example"}
	c.ShippingAddress = &domain.BillingAddress{Line1: "55 Dock Road", City: "Oakland", State: "CA", Zip: "94607", Country: "US"}
	c.BillingAddress = domain.BillingAddress{Line1: "123 Main", Line2: "Suite 400", City: "SF", State: "CA", Zip: "94105", Country: "US"}
	c.Metadata = map[string]any{"contract_tier": "gold", "renewal_month": float64(6), "vip": true}
	if err := repo.Create(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}

	t.Run("round trip", func(t *testing.T) {
		got, err := repo.GetByID(ctxA, c.ID)
		if err != nil || got == nil {
			t.Fatalf("get: %v %v", got, err)
		}
		if got.ExternalID == nil || *got.ExternalID != ext || got.LegalName != c.LegalName || got.Locale != "en-GB" ||
			got.PaymentTerms != "NET_30" || got.Website != c.Website || got.Notes != c.Notes {
			t.Fatalf("scalar fields wrong: %+v", got)
		}
		if len(got.Tags) != 2 || got.Tags[0] != "priority" || len(got.BillingEmails) != 2 {
			t.Fatalf("lists wrong: tags=%v emails=%v", got.Tags, got.BillingEmails)
		}
		if got.ShippingAddress == nil || got.ShippingAddress.City != "Oakland" {
			t.Fatalf("shipping address wrong: %+v", got.ShippingAddress)
		}
		if got.BillingAddress.Line2 != "Suite 400" || got.BillingAddress.Line1 != "123 Main" {
			t.Fatalf("billing address wrong: %+v", got.BillingAddress)
		}
		if got.Metadata["contract_tier"] != "gold" || got.Metadata["renewal_month"] != float64(6) || got.Metadata["vip"] != true {
			t.Fatalf("metadata wrong: %v", got.Metadata)
		}
	})

	t.Run("external_id is unique per workspace", func(t *testing.T) {
		dup := profileCustomer(tenantA, "profile-dup@example.com")
		dup.ExternalID = &ext
		if err := repo.Create(ctx, dup); !errors.Is(err, domain.ErrCustomerExternalIDTaken) {
			t.Fatalf("same tenant, same external_id: want ErrCustomerExternalIDTaken, got %v", err)
		}
		other := profileCustomer(tenantB, "profile-b@example.com")
		other.ExternalID = &ext
		if err := repo.Create(ctx, other); err != nil {
			t.Fatalf("a different workspace may reuse the external_id: %v", err)
		}
	})

	t.Run("update conflict and clear", func(t *testing.T) {
		second := profileCustomer(tenantA, "profile-second@example.com")
		if err := repo.Create(ctx, second); err != nil {
			t.Fatal(err)
		}
		second.ExternalID = &ext
		if err := repo.Update(ctx, second); !errors.Is(err, domain.ErrCustomerExternalIDTaken) {
			t.Fatalf("update to a taken external_id: want ErrCustomerExternalIDTaken, got %v", err)
		}
		// Clearing frees the id for reuse.
		c.ExternalID = nil
		c.Tags = []string{}
		c.Metadata = map[string]any{}
		c.ShippingAddress = nil
		if err := repo.Update(ctx, c); err != nil {
			t.Fatalf("clear: %v", err)
		}
		got, _ := repo.GetByID(ctxA, c.ID)
		if got.ExternalID != nil || len(got.Tags) != 0 || len(got.Metadata) != 0 || got.ShippingAddress != nil {
			t.Fatalf("fields not cleared: %+v", got)
		}
		second.ExternalID = &ext
		if err := repo.Update(ctx, second); err != nil {
			t.Fatalf("external_id should be free after clearing: %v", err)
		}
	})

	t.Run("list filters", func(t *testing.T) {
		tagged := profileCustomer(tenantA, "profile-tagged@example.com")
		tagExt := "TAGGED-1"
		tagged.ExternalID = &tagExt
		tagged.Tags = []string{"vip", "eu"}
		if err := repo.Create(ctx, tagged); err != nil {
			t.Fatal(err)
		}
		byExt, err := repo.List(ctx, tenantA, domain.CustomerFilter{ExternalID: tagExt})
		if err != nil || len(byExt) != 1 || byExt[0].ID != tagged.ID {
			t.Fatalf("external_id filter: %v %v", byExt, err)
		}
		byTag, err := repo.List(ctx, tenantA, domain.CustomerFilter{Tag: "vip"})
		if err != nil || len(byTag) != 1 || byTag[0].ID != tagged.ID {
			t.Fatalf("tag filter: %v %v", byTag, err)
		}
		none, _ := repo.List(ctx, tenantB, domain.CustomerFilter{ExternalID: tagExt})
		if len(none) != 0 {
			t.Fatalf("filter must stay inside the workspace, got %d rows", len(none))
		}
	})

	t.Run("rows created without the new fields read back with defaults", func(t *testing.T) {
		_, legacyID := seedPMTenantCustomer(t, conn.DB)
		got, err := repo.GetByIDPublic(ctx, legacyID)
		if err != nil || got == nil {
			t.Fatalf("legacy get: %v %v", got, err)
		}
		if got.ExternalID != nil || got.LegalName != "" || got.ShippingAddress != nil ||
			got.Tags == nil || len(got.Tags) != 0 || got.BillingEmails == nil || got.Metadata == nil || len(got.Metadata) != 0 {
			t.Fatalf("defaults wrong: %+v", got)
		}
	})
}
