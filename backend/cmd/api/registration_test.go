package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

const registerPath = "/api/v1/auth/register"

func TestRegistrationRejectsInvalidBodies(t *testing.T) {
	router := newRouter(auth.NewService(nil, nil), nil)
	contract := loadContract(t, router)
	for _, test := range []struct {
		name        string
		contentType string
		body        string
		status      int
	}{
		{"duplicate", "application/json", `{"email":"first@example.test","email":"second@example.test","name":"Анна","password":"correct horse battery staple"}`, 400},
		{"unknown", "application/json", `{"email":"first@example.test","name":"Анна","password":"correct horse battery staple","role":"owner"}`, 400},
		{"trailing", "application/json", `{"email":"first@example.test","name":"Анна","password":"correct horse battery staple"} {}`, 400},
		{"weak password", "application/json", `{"email":"first@example.test","name":"Анна","password":"short"}`, 400},
		{"unsupported media", "text/plain", `{}`, 415},
		{"too large", "application/json", strings.Repeat(" ", 4097), 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, registerPath, strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("got status %d, want %d", response.Code, test.status)
			}
			validateResponse(t, contract, request, response)
		})
	}
}

func TestRegisterPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is unset")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal("could not configure test PostgreSQL connection")
	}
	t.Cleanup(pool.Close)
	email := "Register-" + rand.Text() + "@example.test"
	normalizedEmail := strings.ToLower(email)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE email = $1", normalizedEmail); err != nil {
			t.Error("could not remove test registration")
		}
	})

	router := newRouter(auth.NewService(pool, nil), nil)
	contract := loadContract(t, router)
	const password = "correct horse battery staple"
	for _, attempt := range []struct {
		email  string
		status int
	}{
		{email, http.StatusCreated},
		{normalizedEmail, http.StatusConflict},
	} {
		body, err := json.Marshal(registrationRequest{Email: attempt.email, Password: password, Name: "  Анна  "})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, registerPath, strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != attempt.status {
			t.Fatalf("got status %d, want %d", response.Code, attempt.status)
		}
		validateResponse(t, contract, request, response)
		if attempt.status == http.StatusCreated {
			var user auth.User
			if err := json.Unmarshal(response.Body.Bytes(), &user); err != nil {
				t.Fatal(err)
			}
			if user.ID == "" || user.Email != normalizedEmail || user.Name != "Анна" {
				t.Fatal("unexpected registered user")
			}
		}
	}

	var hash string
	err = pool.QueryRow(t.Context(), "SELECT password_hash FROM users WHERE email = $1", normalizedEmail).Scan(&hash)
	if err != nil {
		t.Fatal("could not read registered user from PostgreSQL")
	}
	fields := strings.Split(hash, "$")
	if len(fields) != 6 || fields[1] != "argon2id" || fields[2] != "v=19" || fields[3] != "m=19456,t=2,p=1" {
		t.Fatal("unexpected password hash format")
	}
	salt, err := base64.RawStdEncoding.DecodeString(fields[4])
	if err != nil || len(salt) != 16 {
		t.Fatal("invalid password hash salt")
	}
	key := argon2.IDKey([]byte(password), salt, 2, 19456, 1, 32)
	if base64.RawStdEncoding.EncodeToString(key) != fields[5] {
		t.Fatal("stored password hash does not match the submitted password")
	}
}
