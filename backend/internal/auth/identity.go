package auth

import (
	"context"
	"errors"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrAccessToken = errors.New("invalid access token")

func (s *Service) AccessSubject(raw string) (string, error) {
	if raw == "" || len(raw) > 2048 {
		return "", ErrAccessToken
	}
	claims := new(jwt.RegisteredClaims)
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return s.jwtKey, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(accessIssuer), jwt.WithAudience(accessAudience),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithStrictDecoding())
	if err != nil || claims.IssuedAt == nil || claims.ExpiresAt == nil ||
		!claims.ExpiresAt.After(claims.IssuedAt.Time) || claims.ID == "" {
		return "", ErrAccessToken
	}
	var userID pgtype.UUID
	if err := userID.Scan(claims.Subject); err != nil || claims.Subject != userID.String() {
		return "", ErrAccessToken
	}
	return claims.Subject, nil
}

func (s *Service) UserByID(ctx context.Context, userID string) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `SELECT id::text, email, name FROM users WHERE id = $1::text::uuid`, userID).
		Scan(&user.ID, &user.Email, &user.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrAccessToken
	}
	return user, err
}
