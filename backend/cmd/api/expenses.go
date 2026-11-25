package main

import (
	"encoding/json/v2"
	"net/http"
	"strconv"

	"github.com/Max252t/T-Travels/backend/internal/trips"
	"github.com/gin-gonic/gin"
)

const expenseBodyLimit = 32 << 10

type expenseRequest struct {
	Title        nonNull[string]               `json:"title"`
	Amount       nonNull[int64]                `json:"amount"`
	Category     nonNull[string]               `json:"category"`
	PayerID      nonNull[string]               `json:"payer_id"`
	SplitType    nonNull[string]               `json:"split_type"`
	Date         nonNull[string]               `json:"date"`
	Description  expenseDescription            `json:"description"`
	Participants nonNull[[]expenseParticipant] `json:"participants"`
}

type expenseParticipant struct {
	UserID      nonNull[string] `json:"user_id"`
	Amount      nonNull[int64]  `json:"amount"`
	BasisPoints nonNull[int64]  `json:"basis_points"`
}

type expenseUpdateRequest struct {
	expenseRequest
	Version nonNull[int64] `json:"version"`
}

type expenseDescription struct {
	value   *string
	present bool
}

func (description *expenseDescription) UnmarshalJSON(raw []byte) error {
	description.present = true
	return json.Unmarshal(raw, &description.value)
}

func (request expenseRequest) participants() []trips.ParticipantInput {
	if request.Participants.value == nil {
		return nil
	}
	input := *request.Participants.value
	participants := make([]trips.ParticipantInput, len(input))
	for i, participant := range input {
		participants[i] = trips.ParticipantInput{Amount: participant.Amount.value, BasisPoints: participant.BasisPoints.value}
		if participant.UserID.value != nil {
			participants[i].UserID = *participant.UserID.value
		}
	}
	return participants
}

func (h tripRoutes) tripBudget(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Budget(ctx, c.GetString(userIDContextKey), tripID)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) listExpenses(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	limit, offset, ok := page(c)
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.ListExpenses(ctx, c.GetString(userIDContextKey), tripID, limit, offset)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) getExpense(c *gin.Context) {
	tripID, expenseID, ok := expenseIDs(c)
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.GetExpense(ctx, c.GetString(userIDContextKey), tripID, expenseID)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) createExpense(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}
	request := decodeJSONRequestLimit[expenseRequest](c, expenseBodyLimit)
	if request == nil {
		return
	}
	if request.Title.value == nil || request.Amount.value == nil || request.Category.value == nil || request.PayerID.value == nil ||
		request.SplitType.value == nil || request.Date.value == nil || request.Participants.value == nil {
		writeTripResult(c, 0, nil, trips.ErrInvalidExpense)
		return
	}
	input := trips.ExpenseInput{ExpenseData: trips.ExpenseData{
		Title: *request.Title.value, Amount: *request.Amount.value, Category: *request.Category.value, PayerID: *request.PayerID.value,
		SplitType: *request.SplitType.value, Date: *request.Date.value, Description: request.Description.value,
	}, Participants: request.participants()}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.CreateExpense(ctx, c.GetString(userIDContextKey), tripID, key, input)
	writeTripResult(c, http.StatusCreated, result, err)
}

func (h tripRoutes) updateExpense(c *gin.Context) {
	tripID, expenseID, ok := expenseIDs(c)
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}
	request := decodeJSONRequestLimit[expenseUpdateRequest](c, expenseBodyLimit)
	if request == nil {
		return
	}
	if request.Version.value == nil {
		writeTripResult(c, 0, nil, trips.ErrInvalidExpense)
		return
	}
	patch := trips.ExpensePatch{Title: request.Title.value, Amount: request.Amount.value, Category: request.Category.value,
		PayerID: request.PayerID.value, SplitType: request.SplitType.value, Date: request.Date.value, Description: request.Description.value,
		SetDescription: request.Description.present, Participants: request.participants(), Version: *request.Version.value}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.UpdateExpense(ctx, c.GetString(userIDContextKey), tripID, expenseID, key, patch)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) deleteExpense(c *gin.Context) {
	tripID, expenseID, ok := expenseIDs(c)
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok || !noBody(c) {
		return
	}
	versions := c.Request.URL.Query()["version"]
	if len(versions) != 1 {
		writeTripResult(c, 0, nil, trips.ErrInvalidExpense)
		return
	}
	version, err := strconv.ParseInt(versions[0], 10, 64)
	if err != nil || version < 1 {
		writeTripResult(c, 0, nil, trips.ErrInvalidExpense)
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	err = h.service.DeleteExpense(ctx, c.GetString(userIDContextKey), tripID, expenseID, key, version)
	writeTripResult(c, http.StatusNoContent, nil, err)
}

func expenseIDs(c *gin.Context) (string, string, bool) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return "", "", false
	}
	expenseID, ok := resourceID(c, "expenseId")
	return tripID, expenseID, ok
}
