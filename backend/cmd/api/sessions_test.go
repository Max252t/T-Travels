package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthSessionsPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is unset")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal("could not configure test PostgreSQL connection")
	}
	t.Cleanup(pool.Close)
	key := []byte(rand.Text() + rand.Text())
	api := newRouter(auth.NewService(pool, key))
	contract := loadContract(t, api)
	post := func(path string, value any) (*http.Request, *httptest.ResponseRecorder) {
		body, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/"+path, strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return request, response
	}
	check := func(path string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		request, response := post(path, body)
		if response.Code != status {
			t.Fatalf("%s returned %d, want %d", path, response.Code, status)
		}
		validateResponse(t, contract, request, response)
		return response
	}
	email := rand.Text() + "@example.test"
	password := "correct horse battery staple"
	registered := check("register", registrationRequest{Email: email, Password: password, Name: "Анна"}, http.StatusCreated)
	var user auth.User
	if err := json.Unmarshal(registered.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM refresh_sessions WHERE user_id = $1::text::uuid", user.ID); err != nil {
			t.Error("could not remove test sessions")
		}
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1::text::uuid", user.ID); err != nil {
			t.Error("could not remove test user")
		}
	})

	check("login", loginRequest{Email: email, Password: "a different password"}, http.StatusUnauthorized)
	check("login", loginRequest{Email: "missing-" + email, Password: password}, http.StatusUnauthorized)
	check("login", loginRequest{Email: email}, http.StatusBadRequest)
	login := check("login", loginRequest{Email: email, Password: password}, http.StatusOK)
	readTokens := func(response *httptest.ResponseRecorder) auth.Tokens {
		t.Helper()
		var tokens auth.Tokens
		if err := json.Unmarshal(response.Body.Bytes(), &tokens); err != nil {
			t.Fatal("invalid token response")
		}
		claims := new(jwt.RegisteredClaims)
		_, err := jwt.ParseWithClaims(tokens.AccessToken, claims, func(*jwt.Token) (any, error) { return key, nil },
			jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("t-travels"), jwt.WithAudience("t-travels-api"),
			jwt.WithExpirationRequired(), jwt.WithIssuedAt())
		if err != nil || claims.Subject != user.ID || claims.ID == "" || claims.IssuedAt == nil ||
			claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 15*time.Minute || tokens.ExpiresIn != 900 ||
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("invalid access token claims or response metadata")
		}
		return tokens
	}
	tokens := readTokens(login)
	raw, err := base64.RawURLEncoding.DecodeString(tokens.RefreshToken)
	if err != nil || len(raw) != 32 {
		t.Fatal("invalid refresh token format")
	}
	hash := sha256.Sum256(raw)
	var expiry time.Time
	if err := pool.QueryRow(t.Context(), "SELECT expires_at FROM refresh_sessions WHERE token_hash = $1", hash[:]).Scan(&expiry); err != nil {
		t.Fatal("session does not contain the expected refresh hash")
	}

	type result struct {
		request  *http.Request
		response *httptest.ResponseRecorder
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			request, response := post("refresh", refreshRequest{Token: tokens.RefreshToken})
			results <- result{request, response}
		}()
	}
	var rotated auth.Tokens
	counts := map[int]int{}
	for range 2 {
		attempt := <-results
		validateResponse(t, contract, attempt.request, attempt.response)
		counts[attempt.response.Code]++
		if attempt.response.Code == http.StatusOK {
			rotated = readTokens(attempt.response)
		}
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusUnauthorized] != 1 ||
		rotated.RefreshToken == tokens.RefreshToken || rotated.AccessToken == tokens.AccessToken {
		t.Fatal("concurrent refresh did not rotate the token exactly once")
	}
	var newExpiry time.Time
	if err := pool.QueryRow(t.Context(), "SELECT expires_at FROM refresh_sessions WHERE user_id = $1::text::uuid", user.ID).Scan(&newExpiry); err != nil || !newExpiry.Equal(expiry) {
		t.Fatal("refresh changed the absolute session expiry")
	}
	check("logout", refreshRequest{Token: rotated.RefreshToken}, http.StatusNoContent)
	check("logout", refreshRequest{Token: rotated.RefreshToken}, http.StatusNoContent)
	check("refresh", refreshRequest{Token: rotated.RefreshToken}, http.StatusUnauthorized)
}
