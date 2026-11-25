package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/auth"
	"github.com/gin-gonic/gin"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	Token string `json:"refresh_token"`
}

func loginUser(service *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		request := decodeJSONRequest[loginRequest](c)
		if request == nil {
			return
		}
		if request.Email == "" || request.Password == "" {
			writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Укажите email и пароль")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		tokens, err := service.Login(ctx, request.Email, request.Password)
		writeSessionResult(c, tokens, err)
	}
}

func refreshSession(service *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		request := decodeRefreshRequest(c)
		if request == nil {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		tokens, err := service.Refresh(ctx, request.Token)
		writeSessionResult(c, tokens, err)
	}
}

func logoutSession(service *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		request := decodeRefreshRequest(c)
		if request == nil {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		if err := service.Logout(ctx, request.Token); err != nil {
			writeSessionResult(c, auth.Tokens{}, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func decodeRefreshRequest(c *gin.Context) *refreshRequest {
	request := decodeJSONRequest[refreshRequest](c)
	if request != nil && request.Token == "" {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Укажите refresh-токен")
		return nil
	}
	return request
}

func writeSessionResult(c *gin.Context, tokens auth.Tokens, err error) {
	switch {
	case err == nil:
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, tokens)
	case errors.Is(err, auth.ErrCredentials):
		writeAPIError(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Неверный email или пароль")
	case errors.Is(err, auth.ErrRefreshToken):
		writeAPIError(c, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Refresh-токен недействителен")
	case errors.Is(err, auth.ErrBusy):
		c.Header("Retry-After", "1")
		writeAPIError(c, http.StatusTooManyRequests, "AUTH_BUSY", "Повторите вход позже")
	default:
		slog.Error("session operation failed", "error_type", fmt.Sprintf("%T", err))
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Не удалось выполнить операцию с сессией")
	}
}
