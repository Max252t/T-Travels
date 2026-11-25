package main

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

const profilePath = "/api/v1/users/me"

func TestProfileRejectsInvalidAccessTokens(t *testing.T) {
	key := []byte(rand.Text() + rand.Text())
	api := newRouter(auth.NewService(nil, key))
	contract := loadContract(t, api)
	now := time.Now()
	validClaims := jwt.RegisteredClaims{
		Issuer:    "t-travels",
		Subject:   "dfd03c83-8a17-4b1f-ad37-608a0ac25f4d",
		Audience:  jwt.ClaimStrings{"t-travels-api"},
		ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
		IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
		ID:        rand.Text(),
	}
	sign := func(method jwt.SigningMethod, signingKey []byte, change func(*jwt.RegisteredClaims)) string {
		t.Helper()
		claims := validClaims
		if change != nil {
			change(&claims)
		}
		token, err := jwt.NewWithClaims(method, claims).SignedString(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + token
	}
	for _, test := range []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"incorrect scheme", "Basic example"},
		{"malformed", "Bearer invalid-token"},
		{"wrong signature", sign(jwt.SigningMethodHS256, []byte(rand.Text()+rand.Text()), nil)},
		{"wrong algorithm", sign(jwt.SigningMethodHS384, key, nil)},
		{"expired", sign(jwt.SigningMethodHS256, key, func(c *jwt.RegisteredClaims) {
			c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Second))
		})},
		{"missing expiry", sign(jwt.SigningMethodHS256, key, func(c *jwt.RegisteredClaims) { c.ExpiresAt = nil })},
		{"wrong issuer", sign(jwt.SigningMethodHS256, key, func(c *jwt.RegisteredClaims) { c.Issuer = "other" })},
		{"wrong audience", sign(jwt.SigningMethodHS256, key, func(c *jwt.RegisteredClaims) { c.Audience = jwt.ClaimStrings{"other"} })},
		{"invalid subject", sign(jwt.SigningMethodHS256, key, func(c *jwt.RegisteredClaims) { c.Subject = "not-a-uuid" })},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, profilePath, nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("expected a Bearer challenge, got status %d", response.Code)
			}
			validateResponse(t, contract, request, response)
		})
	}
}
