package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

const (
	accessLifetime  = 15 * time.Minute
	refreshLifetime = 30 * 24 * time.Hour
	refreshBytes    = 32
	accessIssuer    = "t-travels"
	accessAudience  = "t-travels-api"
)

var (
	ErrCredentials  = errors.New("invalid credentials")
	ErrRefreshToken = errors.New("invalid refresh token")
)

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func (s *Service) Login(ctx context.Context, email, password string) (Tokens, error) {
	if !validEmail(email) || !validPassword(password) {
		return Tokens{}, ErrCredentials
	}
	if !s.logins.Allow() {
		return Tokens{}, ErrBusy
	}
	if err := s.acquireHashSlot(ctx); err != nil {
		return Tokens{}, err
	}
	defer func() { <-s.hashSlots }()
	var userID, storedHash string
	err := s.pool.QueryRow(ctx, `SELECT id::text, password_hash FROM users WHERE lower(email) = lower($1)`, email).Scan(&userID, &storedHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, err
	}
	if !verifyPassword(password, storedHash) || userID == "" {
		return Tokens{}, ErrCredentials
	}
	refresh, hash := newRefreshToken()
	tokens, err := s.signTokens(userID, refresh)
	if err != nil {
		return Tokens{}, err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO refresh_sessions (user_id, token_hash, expires_at)
		VALUES ($1::text::uuid, $2, now() + $3::integer * interval '1 second')
	`, userID, hash[:], int32(refreshLifetime/time.Second))
	if err != nil {
		return Tokens{}, err
	}
	return tokens, nil
}

func (s *Service) Refresh(ctx context.Context, token string) (Tokens, error) {
	hash, err := refreshHash(token)
	if err != nil {
		return Tokens{}, err
	}
	refresh, nextHash := newRefreshToken()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Tokens{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var userID string
	err = tx.QueryRow(ctx, `
		UPDATE refresh_sessions SET token_hash = $2
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING user_id::text
	`, hash[:], nextHash[:]).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, ErrRefreshToken
	}
	if err != nil {
		return Tokens{}, err
	}
	tokens, err := s.signTokens(userID, refresh)
	if err != nil {
		return Tokens{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Tokens{}, err
	}
	return tokens, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	hash, err := refreshHash(token)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE refresh_sessions SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
	`, hash[:])
	return err
}

func (s *Service) signTokens(userID, refresh string) (Tokens, error) {
	now := time.Now()
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    accessIssuer,
		Subject:   userID,
		Audience:  jwt.ClaimStrings{accessAudience},
		ExpiresAt: jwt.NewNumericDate(now.Add(accessLifetime)),
		IssuedAt:  jwt.NewNumericDate(now),
		ID:        rand.Text(),
	}).SignedString(s.jwtKey)
	return Tokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(accessLifetime / time.Second), TokenType: "Bearer"}, err
}

func newRefreshToken() (string, [sha256.Size]byte) {
	var raw [refreshBytes]byte
	rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:]), sha256.Sum256(raw[:])
}

func refreshHash(token string) ([sha256.Size]byte, error) {
	if len(token) != base64.RawURLEncoding.EncodedLen(refreshBytes) {
		return [sha256.Size]byte{}, ErrRefreshToken
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != refreshBytes {
		return [sha256.Size]byte{}, ErrRefreshToken
	}
	return sha256.Sum256(raw), nil
}

func verifyPassword(password, storedHash string) bool {
	parts, hasPrefix := strings.CutPrefix(storedHash, passwordHashPrefix)
	encodedSalt, encodedKey, hasKey := strings.Cut(parts, "$")
	salt, saltErr := base64.RawStdEncoding.Strict().DecodeString(encodedSalt)
	key, keyErr := base64.RawStdEncoding.Strict().DecodeString(encodedKey)
	validHash := hasPrefix && hasKey && saltErr == nil && keyErr == nil && len(salt) == argonSaltBytes && len(key) == argonKeyBytes
	if !validHash {
		salt = make([]byte, argonSaltBytes)
		key = make([]byte, argonKeyBytes)
	}
	actual := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyBytes)
	matches := subtle.ConstantTimeCompare(actual, key) == 1
	return validHash && matches
}
