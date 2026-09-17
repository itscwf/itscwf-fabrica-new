// Package middleware holds the gin middleware: JWT authentication and CORS.
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

const (
	// claimsKey is where the verified JWT claims are stored in the context.
	claimsKey = "fabrica.claims"
	// RoleAdmin may perform destructive/administrative operations.
	RoleAdmin = "admin"
	// RoleViewer is read-only.
	RoleViewer = "viewer"
)

// JWTAuth validates `Authorization: Bearer <token>` headers.
type JWTAuth struct {
	manager *fabrica.TokenManager
}

// NewJWTAuth builds the middleware around a token manager.
func NewJWTAuth(manager *fabrica.TokenManager) *JWTAuth { return &JWTAuth{manager: manager} }

// Manager exposes the underlying token manager (used by the auth handlers).
func (a *JWTAuth) Manager() *fabrica.TokenManager { return a.manager }

// Handler rejects requests without a valid Bearer token.
func (a *JWTAuth) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := extractToken(c)
		if raw == "" {
			abort(c, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		claims, err := a.manager.Parse(raw)
		if err != nil {
			abort(c, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		c.Set(claimsKey, claims)
		c.Next()
	}
}

// RequireRole builds a middleware that allows the listed roles only.
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		claims := ClaimsFrom(c)
		if claims == nil {
			abort(c, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		if !allowed[claims.Role] {
			abort(c, http.StatusForbidden, "forbidden", "insufficient role")
			return
		}
		c.Next()
	}
}

// ClaimsFrom returns the verified claims or nil.
func ClaimsFrom(c *gin.Context) *fabrica.Claims {
	if v, ok := c.Get(claimsKey); ok {
		if claims, ok := v.(*fabrica.Claims); ok {
			return claims
		}
	}
	return nil
}

// Subject returns the authenticated username (empty when anonymous).
func Subject(c *gin.Context) string {
	if claims := ClaimsFrom(c); claims != nil {
		if claims.Username != "" {
			return claims.Username
		}
		return claims.Subject
	}
	return "anonymous"
}

// IsAdmin reports whether the caller has the admin role.
func IsAdmin(c *gin.Context) bool {
	claims := ClaimsFrom(c)
	return claims != nil && claims.Role == RoleAdmin
}

func extractToken(c *gin.Context) string {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if header == "" {
		// Also accept ?token= for convenience when linking from the browser.
		return strings.TrimSpace(c.Query("token"))
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return header
}

func abort(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}
