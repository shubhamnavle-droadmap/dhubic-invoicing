package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/recurso-dev/recurso/internal/adapter/httperr"
	"github.com/recurso-dev/recurso/internal/core/domain"
	"github.com/recurso-dev/recurso/internal/service"
)

// PortalAuthMiddleware validates portal session tokens
func PortalAuthMiddleware(portalService *service.PortalService) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check for session token in cookie or header
		token, err := c.Cookie("portal_session")
		if err != nil || token == "" {
			token = c.GetHeader("X-Portal-Session")
		}

		if token == "" {
			httperr.Abort(c, http.StatusUnauthorized, httperr.CodeUnauthorized, "unauthorized")
			return
		}

		// A dedicated-database workspace's session token names the workspace, so
		// the lookup (and everything after it) goes to that workspace's database.
		if tenantID, ok := service.TenantFromSessionToken(token); ok {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), domain.TenantIDKey, tenantID))
		}

		session, err := portalService.ValidateSession(c.Request.Context(), token)
		if err != nil {
			httperr.Abort(c, http.StatusUnauthorized, httperr.CodeUnauthorized, err.Error())
			return
		}

		c.Set("portal_customer_id", session.CustomerID)
		c.Set("portal_session", session)
		c.Next()
	}
}
