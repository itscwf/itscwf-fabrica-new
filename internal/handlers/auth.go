package handlers

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/itscwf/itscwf-fabrica-new/internal/config"
	"github.com/itscwf/itscwf-fabrica-new/internal/middleware"
	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

// authHandlers implementa /api/v1/auth/*.
type authHandlers struct {
	cfg    *config.Config
	tokens *fabrica.TokenManager
	rec    *Recorder
}

func newAuthHandlers(cfg *config.Config, tokens *fabrica.TokenManager, rec *Recorder) *authHandlers {
	return &authHandlers{cfg: cfg, tokens: tokens, rec: rec}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login troca credenciais por um JWT (HS256).
func (a *authHandlers) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, codeBadRequest, "corpo JSON invalido: "+err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !a.validCredentials(req.Username, req.Password) {
		a.rec.RecordAudit(c, req.Username, "login_failed", "auth/"+req.Username, nil)
		fail(c, http.StatusUnauthorized, codeUnauthorized, "credenciais invalidas")
		return
	}
	role := middleware.RoleAdmin
	token, expires, err := a.tokens.Issue(req.Username, role)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "nao foi possivel emitir o token")
		return
	}
	a.rec.RecordAudit(c, req.Username, "login", "auth/"+req.Username, nil)
	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"token":      token,
			"token_type": "Bearer",
			"expires_at": expires.UTC().Format(time.RFC3339),
			"expires_in": int(time.Until(expires).Seconds()),
			"user":       gin.H{"name": req.Username, "username": req.Username, "role": role},
		},
		"token":      token,
		"token_type": "Bearer",
		"expires_at": expires.UTC().Format(time.RFC3339),
		"expires_in": int(time.Until(expires).Seconds()),
		"user":       gin.H{"name": req.Username, "username": req.Username, "role": role},
	})
}

// Me devolve a identidade do token apresentado.
func (a *authHandlers) Me(c *gin.Context) {
	claims := middleware.ClaimsFrom(c)
	if claims == nil {
		fail(c, http.StatusUnauthorized, codeUnauthorized, "token ausente")
		return
	}
	ok(c, http.StatusOK, gin.H{
		"username":   firstNonEmpty(claims.Username, claims.Subject),
		"name":       firstNonEmpty(claims.Username, claims.Subject),
		"role":       claims.Role,
		"issuer":     claims.Issuer,
		"issued_at":  claims.IssuedAt,
		"expires_at": claims.ExpiresAt,
	})
}

// Refresh reemite um token para a identidade atual.
func (a *authHandlers) Refresh(c *gin.Context) {
	claims := middleware.ClaimsFrom(c)
	if claims == nil {
		fail(c, http.StatusUnauthorized, codeUnauthorized, "token ausente")
		return
	}
	username := firstNonEmpty(claims.Username, claims.Subject)
	role := claims.Role
	if role == "" {
		role = middleware.RoleAdmin
	}
	token, expires, err := a.tokens.Issue(username, role)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "nao foi possivel emitir o token")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token":      token,
		"token_type": "Bearer",
		"expires_at": expires.UTC().Format(time.RFC3339),
		"expires_in": int(time.Until(expires).Seconds()),
		"user":       gin.H{"name": username, "username": username, "role": role},
		"data": gin.H{
			"token":      token,
			"token_type": "Bearer",
			"expires_at": expires.UTC().Format(time.RFC3339),
			"expires_in": int(time.Until(expires).Seconds()),
			"user":       gin.H{"name": username, "username": username, "role": role},
		},
	})
}

// validCredentials compara usuario/senha com a configuracao, usando bcrypt
// quando ha hash e comparacao em tempo constante caso contrario.
func (a *authHandlers) validCredentials(username, password string) bool {
	if subtle.ConstantTimeCompare([]byte(username), []byte(a.cfg.Auth.AdminUser)) != 1 {
		return false
	}
	if a.cfg.Auth.AdminPasswordHash != "" {
		return bcrypt.CompareHashAndPassword([]byte(a.cfg.Auth.AdminPasswordHash), []byte(password)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(a.cfg.Auth.AdminPassword)) == 1
}
