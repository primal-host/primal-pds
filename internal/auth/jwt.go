// Package auth provides JWT token management for AT Protocol session
// authentication. Access tokens (2h TTL) authorize XRPC calls, refresh
// tokens (90d TTL) obtain new token pairs.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Token scopes matching the AT Protocol specification.
const (
	ScopeAccess  = "com.atproto.access"
	ScopeRefresh = "com.atproto.refresh"
)

// Token lifetimes.
const (
	AccessTTL  = 2 * time.Hour
	RefreshTTL = 90 * 24 * time.Hour
)

// Claims extends the standard JWT claims with an AT Protocol scope.
type Claims struct {
	jwt.RegisteredClaims
	Scope string `json:"scope"`
}

// TokenPair holds an access/refresh JWT pair returned on login or refresh.
type TokenPair struct {
	AccessJwt  string `json:"accessJwt"`
	RefreshJwt string `json:"refreshJwt"`
}

// JWTManager signs and validates JWT tokens using HS256.
type JWTManager struct {
	secret []byte
	issuer string
}

// NewJWTManager creates a manager with the given HMAC secret and issuer URL.
func NewJWTManager(secret, issuer string) *JWTManager {
	return &JWTManager{
		secret: []byte(secret),
		issuer: issuer,
	}
}

// GenerateSecret returns a random 32-byte hex string for use as a JWT secret.
func GenerateSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateTokenPair generates an access/refresh token pair for the given DID.
func (m *JWTManager) CreateTokenPair(did string) (*TokenPair, error) {
	now := time.Now()

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   did,
			Issuer:    m.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTTL)),
		},
		Scope: ScopeAccess,
	})
	accessStr, err := accessToken.SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("auth: sign access token: %w", err)
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   did,
			Issuer:    m.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(RefreshTTL)),
		},
		Scope: ScopeRefresh,
	})
	refreshStr, err := refreshToken.SignedString(m.secret)
	if err != nil {
		return nil, fmt.Errorf("auth: sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessJwt:  accessStr,
		RefreshJwt: refreshStr,
	}, nil
}

// ValidateAccessToken parses and validates a JWT access token, returning
// the subject DID. Returns an error if the token is invalid, expired, or
// has the wrong scope.
func (m *JWTManager) ValidateAccessToken(tokenStr string) (string, error) {
	return m.validate(tokenStr, ScopeAccess)
}

// ValidateRefreshToken parses and validates a JWT refresh token, returning
// the subject DID. Returns an error if the token is invalid, expired, or
// has the wrong scope.
func (m *JWTManager) ValidateRefreshToken(tokenStr string) (string, error) {
	return m.validate(tokenStr, ScopeRefresh)
}

func (m *JWTManager) validate(tokenStr, expectedScope string) (string, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return "", fmt.Errorf("auth: invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return "", fmt.Errorf("auth: invalid token claims")
	}

	if claims.Scope != expectedScope {
		return "", fmt.Errorf("auth: wrong scope: got %q, want %q", claims.Scope, expectedScope)
	}

	if claims.Subject == "" {
		return "", fmt.Errorf("auth: missing subject")
	}

	return claims.Subject, nil
}
