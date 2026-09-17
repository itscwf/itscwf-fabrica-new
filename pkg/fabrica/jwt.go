package fabrica

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	// ErrInvalidToken is returned for any malformed or mis-signed token.
	ErrInvalidToken = errors.New("invalid token")
	// ErrExpiredToken is returned when the token is well formed but expired.
	ErrExpiredToken = errors.New("expired token")
)

// Claims is the JWT payload issued by /api/v1/auth/login.
type Claims struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// TokenManager issues and verifies HS256 tokens.
type TokenManager struct {
	secret []byte
	issuer string
	ttl    time.Duration
}

// NewTokenManager validates the secret and builds the manager.
func NewTokenManager(secret, issuer string, ttl time.Duration) (*TokenManager, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("jwt secret must be at least 16 characters")
	}
	if issuer == "" {
		issuer = "itscwf-fabrica"
	}
	// A negative TTL is honoured on purpose so tests can mint expired tokens.
	if ttl == 0 {
		ttl = 12 * time.Hour
	}
	return &TokenManager{secret: []byte(secret), issuer: issuer, ttl: ttl}, nil
}

// TTL returns the configured token lifetime.
func (m *TokenManager) TTL() time.Duration { return m.ttl }

// Issue signs a token for the given identity and returns it with its expiry.
func (m *TokenManager) Issue(username, role string) (string, time.Time, error) {
	now := time.Now().UTC()
	expires := now.Add(m.ttl)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			Issuer:    m.issuer,
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	})
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expires, nil
}

// Parse verifies the signature, issuer, algorithm and expiry.
func (m *TokenManager) Parse(raw string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return m.secret, nil
	},
		jwt.WithIssuer(m.issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("%w: missing subject", ErrInvalidToken)
	}
	return claims, nil
}
