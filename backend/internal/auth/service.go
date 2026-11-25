package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonKeyBytes  = 32
	argonSaltBytes = 16
)

var (
	ErrEmailTaken = errors.New("email already registered")
	ErrBusy       = errors.New("registration capacity reached")
)

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type Service struct {
	pool      *pgxpool.Pool
	hashSlots chan struct{}
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, hashSlots: make(chan struct{}, 4)}
}

func (s *Service) Register(ctx context.Context, email, password, name string) (User, error) {
	address, err := mail.ParseAddress(email)
	if err != nil || len(email) > 254 || strings.ContainsRune(email, 0) || address.Address != email {
		return User{}, &ValidationError{Message: "Укажите корректный email"}
	}
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsRune(name, 0) {
		return User{}, &ValidationError{Message: "Имя должно содержать от 1 до 100 символов"}
	}
	if length := utf8.RuneCountInString(password); length < 15 || length > 128 {
		return User{}, &ValidationError{Message: "Пароль должен содержать от 15 до 128 символов"}
	}

	select {
	case <-ctx.Done():
		return User{}, ctx.Err()
	case s.hashSlots <- struct{}{}:
	default:
		return User{}, ErrBusy
	}
	defer func() { <-s.hashSlots }()
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	hash := hashPassword(password)
	var user User
	err = s.pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, name)
		VALUES (lower($1), $2, $3)
		ON CONFLICT (lower(email)) DO NOTHING
		RETURNING id::text, email, name
	`, email, hash, trimmedName).Scan(&user.ID, &user.Email, &user.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrEmailTaken
	}
	return user, err
}

func hashPassword(password string) string {
	var salt [argonSaltBytes]byte
	rand.Read(salt[:])
	key := argon2.IDKey([]byte(password), salt[:], argonTime, argonMemoryKiB, argonThreads, argonKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt[:]), base64.RawStdEncoding.EncodeToString(key))
}
