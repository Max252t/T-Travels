package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/auth"
	"github.com/gin-gonic/gin"
)

const userIDContextKey = "user_id"

func requireAccessToken(service *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		headers := c.Request.Header.Values("Authorization")
		if len(headers) != 1 {
			unauthorized(c)
			return
		}
		scheme, raw, ok := strings.Cut(headers[0], " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			unauthorized(c)
			return
		}
		id, err := service.AccessSubject(strings.TrimSpace(raw))
		if err != nil {
			unauthorized(c)
			return
		}
		c.Set(userIDContextKey, id)
		c.Next()
	}
}

func getCurrentUser(service *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		user, err := service.UserByID(ctx, c.GetString(userIDContextKey))
		switch {
		case err == nil:
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusOK, user)
		case errors.Is(err, auth.ErrAccessToken):
			unauthorized(c)
		default:
			slog.Error("could not read current user", "error_type", fmt.Sprintf("%T", err))
			writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Не удалось получить профиль")
		}
	}
}

func unauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", "Bearer")
	c.Abort()
	writeAPIError(c, http.StatusUnauthorized, "INVALID_ACCESS_TOKEN", "Требуется действующий access-токен")
}
