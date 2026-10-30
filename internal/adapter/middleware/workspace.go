package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

// WorkspaceQueryParam is the link parameter that names the workspace on public,
// unauthenticated pages (hosted checkout, payment order, portal login).
const WorkspaceQueryParam = "w"

// WorkspaceFromQuery puts the workspace named by ?w=<tenant-uuid> on the request
// context, so database routing can pick that workspace's dedicated database
// before the handler looks anything up. Public endpoints have no session or API
// key to take the tenant from, and the invoice id alone does not say which
// database holds it.
//
// The value is untrusted. It only selects where to look; handlers still confirm
// the record they find belongs to that workspace. An unknown workspace resolves
// to the shared database, where the lookup finds nothing. A malformed value is
// rejected. Without the parameter nothing changes.
func WorkspaceFromQuery() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.Query(WorkspaceQueryParam)
		if raw == "" {
			c.Next()
			return
		}
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid workspace"})
			return
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), domain.TenantIDKey, id))
		c.Next()
	}
}

// WithWorkspaceParam appends ?w=<tenant> (or &w= when link already has a query).
func WithWorkspaceParam(link string, tenantID uuid.UUID) string {
	sep := "?"
	if strings.Contains(link, "?") {
		sep = "&"
	}
	return link + sep + WorkspaceQueryParam + "=" + tenantID.String()
}
