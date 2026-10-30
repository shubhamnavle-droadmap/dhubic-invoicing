package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
	"github.com/recurso-dev/recurso/internal/core/port"
	"github.com/recurso-dev/recurso/internal/service"
)

// profileRepo is an in-memory customer store that enforces external_id
// uniqueness per tenant, like the real unique index does.
type profileRepo struct {
	port.CustomerRepository
	rows map[uuid.UUID]*domain.Customer
}

func newProfileRepo() *profileRepo { return &profileRepo{rows: map[uuid.UUID]*domain.Customer{}} }

func (r *profileRepo) taken(c *domain.Customer) bool {
	if c.ExternalID == nil {
		return false
	}
	for _, o := range r.rows {
		if o.ID != c.ID && o.TenantID == c.TenantID && o.ExternalID != nil && *o.ExternalID == *c.ExternalID {
			return true
		}
	}
	return false
}

func (r *profileRepo) Create(_ context.Context, c *domain.Customer) error {
	if r.taken(c) {
		return domain.ErrCustomerExternalIDTaken
	}
	cp := *c
	r.rows[c.ID] = &cp
	return nil
}

func (r *profileRepo) Update(_ context.Context, c *domain.Customer) error {
	if r.taken(c) {
		return domain.ErrCustomerExternalIDTaken
	}
	cp := *c
	r.rows[c.ID] = &cp
	return nil
}

func (r *profileRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Customer, error) {
	if c, ok := r.rows[id]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, nil
}

func doCustomerCall(t *testing.T, call func(*gin.Context), method, path string, tenant uuid.UUID, params gin.Params, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("tenant_id", tenant)
	c.Params = params
	call(c)
	return w
}

func decodeCustomer(t *testing.T, w *httptest.ResponseRecorder) domain.Customer {
	t.Helper()
	var out struct {
		Data domain.Customer `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body %s", err, w.Body.String())
	}
	return out.Data
}

func TestCreateCustomer_ProfileFields(t *testing.T) {
	repo := newProfileRepo()
	h := NewCustomerHandler(service.NewCustomerService(repo), nil)
	tenant := uuid.New()

	w := doCustomerCall(t, h.CreateCustomer, http.MethodPost, "/v1/customers", tenant, nil, `{
		"email": "a@example.com", "name": "Acme",
		"external_id": "CUST-1", "legal_name": "Acme Inc.", "locale": "en",
		"payment_terms": "NET_30", "website": "https://acme.example", "notes": "hello",
		"tags": ["vip"], "billing_emails": ["ap@acme.example"],
		"billing_address": {"line1": "1 Main", "line2": "Suite 4", "country": "US"},
		"shipping_address": {"line1": "55 Dock", "city": "Oakland", "country": "US"},
		"metadata": {"tier": "gold", "seats": 5}
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d; body %s", w.Code, w.Body.String())
	}
	got := decodeCustomer(t, w)
	if got.ExternalID == nil || *got.ExternalID != "CUST-1" || got.LegalName != "Acme Inc." || got.PaymentTerms != "NET_30" {
		t.Fatalf("profile fields not returned: %+v", got)
	}
	if got.BillingAddress.Line2 != "Suite 4" || got.ShippingAddress == nil || got.ShippingAddress.City != "Oakland" {
		t.Fatalf("addresses wrong: %+v / %+v", got.BillingAddress, got.ShippingAddress)
	}
	if got.Metadata["tier"] != "gold" || got.Metadata["seats"] != float64(5) {
		t.Fatalf("metadata wrong: %v", got.Metadata)
	}
}

func TestCreateCustomer_WithoutProfileFieldsStillWorks(t *testing.T) {
	repo := newProfileRepo()
	h := NewCustomerHandler(service.NewCustomerService(repo), nil)
	w := doCustomerCall(t, h.CreateCustomer, http.MethodPost, "/v1/customers", uuid.New(), nil,
		`{"email": "plain@example.com", "name": "Plain"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d; body %s", w.Code, w.Body.String())
	}
	got := decodeCustomer(t, w)
	if got.ExternalID != nil || len(got.Metadata) != 0 || got.ShippingAddress != nil {
		t.Fatalf("unexpected profile data: %+v", got)
	}
}

func TestCreateCustomer_ProfileValidationAndConflict(t *testing.T) {
	repo := newProfileRepo()
	h := NewCustomerHandler(service.NewCustomerService(repo), nil)
	tenant := uuid.New()
	create := func(body string) *httptest.ResponseRecorder {
		return doCustomerCall(t, h.CreateCustomer, http.MethodPost, "/v1/customers", tenant, nil, body)
	}

	if w := create(`{"email":"a@example.com","name":"A","external_id":"X1"}`); w.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", w.Code, w.Body.String())
	}
	if w := create(`{"email":"b@example.com","name":"B","external_id":"X1"}`); w.Code != http.StatusConflict {
		t.Fatalf("duplicate external_id: got %d want 409; %s", w.Code, w.Body.String())
	}

	bad := map[string]string{
		"nested metadata":   `{"email":"c@example.com","name":"C","metadata":{"a":{"b":1}}}`,
		"long metadata":     `{"email":"c@example.com","name":"C","metadata":{"a":"` + strings.Repeat("x", 501) + `"}}`,
		"bad billing email": `{"email":"c@example.com","name":"C","billing_emails":["nope"]}`,
		"bad locale":        `{"email":"c@example.com","name":"C","locale":"not a locale"}`,
	}
	for name, body := range bad {
		if w := create(body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d want 400; %s", name, w.Code, w.Body.String())
		}
	}
}

func TestUpdateCustomer_MetadataMergesAndNullRemoves(t *testing.T) {
	repo := newProfileRepo()
	h := NewCustomerHandler(service.NewCustomerService(repo), nil)
	tenant := uuid.New()

	w := doCustomerCall(t, h.CreateCustomer, http.MethodPost, "/v1/customers", tenant, nil,
		`{"email":"m@example.com","name":"M","external_id":"M1","tags":["a","b"],"metadata":{"keep":"1","drop":"2"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id := decodeCustomer(t, w).ID.String()
	update := func(body string) *httptest.ResponseRecorder {
		return doCustomerCall(t, h.UpdateCustomer, http.MethodPut, "/v1/customers/"+id, tenant, gin.Params{{Key: "id", Value: id}}, body)
	}

	w = update(`{"metadata":{"drop":null,"added":"3"},"notes":"updated"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	got := decodeCustomer(t, w)
	if got.Metadata["keep"] != "1" || got.Metadata["added"] != "3" {
		t.Fatalf("merge lost or missed keys: %v", got.Metadata)
	}
	if _, ok := got.Metadata["drop"]; ok {
		t.Fatalf("null must remove the key: %v", got.Metadata)
	}
	// Fields not in the body are left alone.
	if got.ExternalID == nil || *got.ExternalID != "M1" || len(got.Tags) != 2 || got.Notes != "updated" {
		t.Fatalf("untouched fields changed: %+v", got)
	}

	// Explicit empties clear: external_id "", tags [].
	w = update(`{"external_id":"","tags":[]}`)
	got = decodeCustomer(t, w)
	if got.ExternalID != nil || len(got.Tags) != 0 {
		t.Fatalf("clear failed: %+v", got)
	}
}

func TestUpdateCustomer_ExternalIDConflictIs409(t *testing.T) {
	repo := newProfileRepo()
	h := NewCustomerHandler(service.NewCustomerService(repo), nil)
	tenant := uuid.New()
	mk := func(email, ext string) string {
		w := doCustomerCall(t, h.CreateCustomer, http.MethodPost, "/v1/customers", tenant, nil,
			`{"email":"`+email+`","name":"N","external_id":"`+ext+`"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", ext, w.Code, w.Body.String())
		}
		return decodeCustomer(t, w).ID.String()
	}
	mk("one@example.com", "E1")
	id2 := mk("two@example.com", "E2")

	w := doCustomerCall(t, h.UpdateCustomer, http.MethodPut, "/v1/customers/"+id2, tenant,
		gin.Params{{Key: "id", Value: id2}}, `{"external_id":"E1"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d want 409; %s", w.Code, w.Body.String())
	}
}

func TestListCustomers_PassesProfileFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := &filterRecordingRepo{}
	h := NewCustomerHandler(service.NewCustomerService(rec), &noSubs{})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/customers?external_id=CUST-9&tag=vip", nil)
	c.Set("tenant_id", uuid.New())
	h.ListCustomers(c)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d; %s", w.Code, w.Body.String())
	}
	if rec.filter.ExternalID != "CUST-9" || rec.filter.Tag != "vip" {
		t.Fatalf("filters not passed through: %+v", rec.filter)
	}
}

type filterRecordingRepo struct {
	port.CustomerRepository
	filter domain.CustomerFilter
}

func (r *filterRecordingRepo) List(_ context.Context, _ uuid.UUID, f domain.CustomerFilter) ([]*domain.Customer, error) {
	r.filter = f
	return nil, nil
}

type noSubs struct{ port.SubscriptionRepository }

func (noSubs) CountActiveByCustomer(context.Context, uuid.UUID) (map[uuid.UUID]int, error) {
	return map[uuid.UUID]int{}, nil
}
