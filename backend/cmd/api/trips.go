package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/Max252t/T-Travels/backend/internal/trips"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
)

type tripRoutes struct {
	service      *trips.Service
	receiptSlots chan struct{}
}

func registerTripRoutes(api *gin.RouterGroup, service *trips.Service) {
	h := tripRoutes{service: service, receiptSlots: make(chan struct{}, 4)}
	api.GET("/trips", h.list)
	api.POST("/trips", h.create)
	api.POST("/trips/join", h.join)
	api.GET("/trips/:tripId", h.get)
	api.PATCH("/trips/:tripId", h.update)
	api.GET("/trips/:tripId/members", h.members)
	api.DELETE("/trips/:tripId/members/:userId", h.removeMember)
	api.POST("/trips/:tripId/leave", h.leaveTrip)
	api.POST("/trips/:tripId/invitations", h.invite)
	api.GET("/trips/:tripId/budget", h.tripBudget)
	api.GET("/trips/:tripId/sync", h.syncTrip)
	api.GET("/trips/:tripId/expenses", h.listExpenses)
	api.POST("/trips/:tripId/expenses", h.createExpense)
	api.GET("/trips/:tripId/expenses/:expenseId", h.getExpense)
	api.PATCH("/trips/:tripId/expenses/:expenseId", h.updateExpense)
	api.DELETE("/trips/:tripId/expenses/:expenseId", h.deleteExpense)
	api.GET("/trips/:tripId/expenses/:expenseId/receipt", h.getReceipt)
	api.POST("/trips/:tripId/expenses/:expenseId/receipt", h.putReceipt)
	api.DELETE("/trips/:tripId/expenses/:expenseId/receipt", h.deleteReceipt)
	api.POST("/trips/:tripId/expenses/:expenseId/dispute", h.openDispute)
	api.POST("/trips/:tripId/expenses/:expenseId/dispute/resolve", h.resolveDispute)
	api.GET("/trips/:tripId/balances", h.balances)
	api.GET("/trips/:tripId/settlements/suggestions", h.settlementSuggestions)
	api.GET("/trips/:tripId/settlements", h.listSettlements)
	api.POST("/trips/:tripId/settlements", h.createSettlement)
	api.GET("/notifications", h.notifications)
	api.PATCH("/notifications/:notificationId/read", h.readNotification)
	api.POST("/notifications/read-all", h.readAllNotifications)
}

type nonNull[T any] struct{ value *T }

func (f *nonNull[T]) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		return trips.ErrInvalid
	}
	var value T
	if err := json.Unmarshal(raw, &value, json.RejectUnknownMembers(true)); err != nil {
		return err
	}
	f.value = &value
	return nil
}

type tripFieldsRequest struct {
	Name        nonNull[string] `json:"name"`
	Destination nonNull[string] `json:"destination"`
	StartDate   nonNull[string] `json:"start_date"`
	EndDate     nonNull[string] `json:"end_date"`
	Budget      nonNull[int64]  `json:"budget"`
}

type tripUpdateRequest struct {
	tripFieldsRequest
	Version nonNull[int64] `json:"version"`
}

func (h tripRoutes) list(c *gin.Context) {
	limit, offset, ok := page(c)
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.List(ctx, c.GetString(userIDContextKey), limit, offset)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) create(c *gin.Context) {
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}
	request := decodeJSONRequest[tripFieldsRequest](c)
	if request == nil {
		return
	}
	if request.Name.value == nil || request.Destination.value == nil || request.StartDate.value == nil ||
		request.EndDate.value == nil || request.Budget.value == nil {
		writeTripResult(c, 0, nil, trips.ErrInvalid)
		return
	}
	input := trips.Input{Name: *request.Name.value, Destination: *request.Destination.value,
		StartDate: *request.StartDate.value, EndDate: *request.EndDate.value, Budget: *request.Budget.value}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Create(ctx, c.GetString(userIDContextKey), key, input)
	writeTripResult(c, http.StatusCreated, result, err)
}

func (h tripRoutes) get(c *gin.Context) {
	id, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Get(ctx, c.GetString(userIDContextKey), id)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) update(c *gin.Context) {
	id, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}
	request := decodeJSONRequest[tripUpdateRequest](c)
	if request == nil {
		return
	}
	if request.Version.value == nil {
		writeTripResult(c, 0, nil, trips.ErrInvalid)
		return
	}
	patch := trips.Patch{Name: request.Name.value, Destination: request.Destination.value, StartDate: request.StartDate.value,
		EndDate: request.EndDate.value, Budget: request.Budget.value, Version: *request.Version.value}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Update(ctx, c.GetString(userIDContextKey), id, key, patch)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) members(c *gin.Context) {
	id, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	limit, offset, ok := page(c)
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Members(ctx, c.GetString(userIDContextKey), id, limit, offset)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) invite(c *gin.Context) {
	id, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok || !noBody(c) {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Invite(ctx, c.GetString(userIDContextKey), id, key)
	writeTripResult(c, http.StatusCreated, result, err)
}

type joinTripRequest struct {
	Code string `json:"code"`
}

func (h tripRoutes) join(c *gin.Context) {
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}
	request := decodeJSONRequest[joinTripRequest](c)
	if request == nil {
		return
	}
	if request.Code == "" {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Укажите код приглашения")
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Join(ctx, c.GetString(userIDContextKey), key, request.Code)
	writeTripResult(c, http.StatusOK, result, err)
}

func tripContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 5*time.Second)
}

func noBody(c *gin.Context) bool {
	if c.Request.ContentLength != 0 {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Запрос не должен содержать тело")
		return false
	}
	return true
}

func page(c *gin.Context) (int32, int32, bool) {
	limit, limitErr := strconv.ParseInt(c.DefaultQuery("limit", "50"), 10, 32)
	offset, offsetErr := strconv.ParseInt(c.DefaultQuery("offset", "0"), 10, 32)
	if limitErr != nil || offsetErr != nil || limit < 1 || limit > 100 || offset < 0 {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Некорректные параметры пагинации")
		return 0, 0, false
	}
	return int32(limit), int32(offset), true
}

func resourceID(c *gin.Context, param string) (string, bool) {
	id, ok := canonicalUUID(c.Param(param))
	if !ok {
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Идентификатор ресурса должен быть UUID")
	}
	return id, ok
}

func idempotencyKey(c *gin.Context) (string, bool) {
	values := c.Request.Header.Values("Idempotency-Key")
	if len(values) == 1 {
		if id, ok := canonicalUUID(values[0]); ok {
			return id, true
		}
	}
	writeAPIError(c, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Укажите один UUID в заголовке Idempotency-Key")
	return "", false
}

func canonicalUUID(raw string) (string, bool) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil || !strings.EqualFold(raw, id.String()) {
		return "", false
	}
	return id.String(), true
}

func writeTripResult(c *gin.Context, status int, result any, err error) {
	switch {
	case err == nil:
		c.Header("Cache-Control", "no-store")
		if status == http.StatusNoContent {
			c.Status(status)
		} else {
			c.JSON(status, result)
		}
	case errors.Is(err, trips.ErrInvalid):
		writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Проверьте параметры поездки")
	case errors.Is(err, trips.ErrNotFound):
		writeAPIError(c, http.StatusNotFound, "TRIP_NOT_FOUND", "Поездка не найдена")
	case errors.Is(err, trips.ErrNotificationNotFound):
		writeAPIError(c, http.StatusNotFound, "NOTIFICATION_NOT_FOUND", "Уведомление не найдено")
	case errors.Is(err, trips.ErrForbidden):
		writeAPIError(c, http.StatusForbidden, "OWNER_REQUIRED", "Действие доступно организатору")
	case errors.Is(err, trips.ErrInvitation):
		writeAPIError(c, http.StatusNotFound, "INVITATION_NOT_FOUND", "Приглашение недействительно или истекло")
	case errors.Is(err, trips.ErrVersion):
		writeAPIError(c, http.StatusConflict, "VERSION_CONFLICT", "Поездка уже была изменена")
	case errors.Is(err, trips.ErrInvalidExpense):
		writeAPIError(c, http.StatusBadRequest, "INVALID_EXPENSE", "Проверьте параметры расхода и доли участников")
	case errors.Is(err, trips.ErrExpenseNotFound):
		writeAPIError(c, http.StatusNotFound, "EXPENSE_NOT_FOUND", "Расход не найден")
	case errors.Is(err, trips.ErrExpenseForbidden):
		writeAPIError(c, http.StatusForbidden, "EXPENSE_EDITOR_REQUIRED", "Изменить расход может его автор или организатор")
	case errors.Is(err, trips.ErrExpenseVersion):
		writeAPIError(c, http.StatusConflict, "VERSION_CONFLICT", "Расход уже был изменён")
	case errors.Is(err, trips.ErrExpenseLimit):
		writeAPIError(c, http.StatusConflict, "EXPENSE_LIMIT_EXCEEDED", "Сумма расходов поездки превышает допустимый предел")
	case errors.Is(err, trips.ErrInvalidSettlement):
		writeAPIError(c, http.StatusBadRequest, "INVALID_SETTLEMENT", "Проверьте получателя, сумму и версию рекомендаций")
	case errors.Is(err, trips.ErrSettlementRevision):
		writeAPIError(c, http.StatusConflict, "SETTLEMENT_REVISION_CONFLICT", "Обновите рекомендации переводов")
	case errors.Is(err, trips.ErrSettlementUnavailable):
		writeAPIError(c, http.StatusConflict, "SETTLEMENT_UNAVAILABLE", "Этот перевод или сумма отсутствуют в текущих рекомендациях")
	case errors.Is(err, trips.ErrInvalidDispute):
		writeAPIError(c, http.StatusBadRequest, "INVALID_DISPUTE", "Укажите причину спора длиной от 1 до 1000 символов")
	case errors.Is(err, trips.ErrOpenDispute):
		writeAPIError(c, http.StatusConflict, "EXPENSE_DISPUTE_OPEN", "У расхода уже есть открытый спор")
	case errors.Is(err, trips.ErrNoDispute):
		writeAPIError(c, http.StatusNotFound, "DISPUTE_NOT_FOUND", "Открытый спор не найден")
	case errors.Is(err, trips.ErrDisputeAuthor):
		writeAPIError(c, http.StatusForbidden, "DISPUTE_AUTHOR_REQUIRED", "Закрыть спор может его инициатор")
	case errors.Is(err, trips.ErrDisputeAcknowledgement):
		writeAPIError(c, http.StatusConflict, "DISPUTE_ACKNOWLEDGEMENT_REQUIRED", "Подтвердите перевод с открытыми спорами")
	case errors.Is(err, trips.ErrMemberNotFound):
		writeAPIError(c, http.StatusNotFound, "MEMBER_NOT_FOUND", "Участник поездки не найден")
	case errors.Is(err, trips.ErrOwnerDeparture):
		writeAPIError(c, http.StatusConflict, "TRIP_OWNER_REQUIRED", "Организатор должен остаться участником поездки")
	case errors.Is(err, trips.ErrMemberBalance):
		writeAPIError(c, http.StatusConflict, "NONZERO_MEMBER_BALANCE", "Сначала погасите баланс участника")
	case errors.Is(err, trips.ErrMemberDisputes):
		writeAPIError(c, http.StatusConflict, "MEMBER_DISPUTE_OPEN", "Участник должен закрыть свои открытые споры")
	case errors.Is(err, trips.ErrInactiveBalance):
		writeAPIError(c, http.StatusConflict, "INACTIVE_MEMBER_BALANCE", "Изменение затронет баланс бывшего участника")
	case errors.Is(err, trips.ErrReceiptNotFound):
		writeAPIError(c, http.StatusNotFound, "RECEIPT_NOT_FOUND", "Чек не найден")
	case errors.Is(err, trips.ErrReceiptStorage):
		writeAPIError(c, http.StatusServiceUnavailable, "RECEIPT_STORAGE_UNAVAILABLE", "Хранилище чеков временно недоступно")
	case errors.Is(err, trips.ErrSnapshotLimit):
		writeAPIError(c, http.StatusConflict, "SNAPSHOT_LIMIT_EXCEEDED", "Используйте постраничные запросы для этой поездки")
	case errors.Is(err, idempotency.ErrKeyReused):
		writeAPIError(c, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "Ключ уже использован для другого запроса")
	case errors.Is(err, trips.ErrRateLimit):
		c.Header("Retry-After", "1")
		writeAPIError(c, http.StatusTooManyRequests, "JOIN_BUSY", "Повторите присоединение позже")
	default:
		slog.Error("trip API request failed", "error_type", fmt.Sprintf("%T", err))
		writeAPIError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Не удалось выполнить операцию")
	}
}
