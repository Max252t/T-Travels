package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type disputeRequest struct {
	Reason string `json:"reason"`
}

func (h tripRoutes) openDispute(c *gin.Context) {
	tripID, expenseID, ok := expenseIDs(c)
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}
	input := decodeJSONRequest[disputeRequest](c)
	if input == nil {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.OpenDispute(ctx, c.GetString(userIDContextKey), tripID, expenseID, key, input.Reason)
	writeTripResult(c, http.StatusCreated, result, err)
}

func (h tripRoutes) resolveDispute(c *gin.Context) {
	tripID, expenseID, ok := expenseIDs(c)
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok || !noBody(c) {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.ResolveDispute(ctx, c.GetString(userIDContextKey), tripID, expenseID, key)
	writeTripResult(c, http.StatusOK, result, err)
}
