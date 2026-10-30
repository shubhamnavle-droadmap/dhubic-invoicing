package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

func TestWorkspaceFromQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", WorkspaceFromQuery(), func(c *gin.Context) {
		id, _ := c.Request.Context().Value(domain.TenantIDKey).(uuid.UUID)
		c.String(http.StatusOK, id.String())
	})
	want := uuid.New()

	do := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x"+q, nil))
		return w
	}
	if w := do("?w=" + want.String()); w.Code != 200 || w.Body.String() != want.String() {
		t.Errorf("valid: %d %q", w.Code, w.Body.String())
	}
	if w := do(""); w.Code != 200 || w.Body.String() != uuid.Nil.String() {
		t.Errorf("absent must leave the context untouched: %d %q", w.Code, w.Body.String())
	}
	for _, bad := range []string{"?w=nope", "?w=" + uuid.Nil.String(), "?w=%27%3B--"} {
		if w := do(bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, want 400", bad, w.Code)
		}
	}
}

func TestWithWorkspaceParam(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	if got, want := WithWorkspaceParam("https://app/checkout/abc", id), "https://app/checkout/abc?w="+id.String(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got, want := WithWorkspaceParam("https://app/x?a=1", id), "https://app/x?a=1&w="+id.String(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
