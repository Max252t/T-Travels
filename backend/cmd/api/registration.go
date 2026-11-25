package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/auth"
	"github.com/gin-gonic/gin"
)

type registrationRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func registerUser(service *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		request := decodeJSONRequest[registrationRequest](c)
		if request == nil {
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		user, err := service.Register(ctx, request.Email, request.Password, request.Name)
		var validation *auth.ValidationError
		switch {
		case err == nil:
			c.JSON(http.StatusCreated, user)
		case errors.As(err, &validation):
			writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", validation.Message)
		case errors.Is(err, auth.ErrEmailTaken):
			writeAPIError(c, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "Этот email уже зарегистрирован")
		case errors.Is(err, auth.ErrBusy):
			c.Header("Retry-After", "1")
			writeAPIError(c, http.StatusTooManyRequests, "REGISTRATION_BUSY", "Повторите регистрацию позже")
		default:
			slog.Error("registration failed", "error_type", fmt.Sprintf("%T", err))
			writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Не удалось зарегистрировать пользователя")
		}
	}
}

func decodeJSONRequest[T any](c *gin.Context) *T {
	return decodeJSONRequestLimit[T](c, 4<<10)
}

func decodeJSONRequestLimit[T any](c *gin.Context, limit int64) *T {
	mediaType, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	charset := params["charset"]
	encodings := c.Request.Header.Values("Content-Encoding")
	if err != nil || mediaType != "application/json" || (charset != "" && !strings.EqualFold(charset, "utf-8")) ||
		len(encodings) > 1 || (len(encodings) == 1 && !strings.EqualFold(encodings[0], "identity")) {
		writeAPIError(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Ожидается JSON в UTF-8 без сжатия")
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, limit))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeAPIError(c, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", fmt.Sprintf("Размер запроса превышает %d КиБ", limit>>10))
		return nil
	}
	var request *T
	if err != nil || json.Unmarshal(body, &request, json.RejectUnknownMembers(true)) != nil || request == nil {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Некорректное тело запроса")
		return nil
	}
	return request
}

func writeAPIError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}
