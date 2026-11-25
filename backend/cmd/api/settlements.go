package main

import (
	"net/http"

	"github.com/Max252t/T-Travels/backend/internal/trips"
	"github.com/gin-gonic/gin"
)

func (h tripRoutes) balances(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Balances(ctx, c.GetString(userIDContextKey), tripID)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) settlementSuggestions(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.SettlementSuggestions(ctx, c.GetString(userIDContextKey), tripID)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) listSettlements(c *gin.Context) {
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
	result, err := h.service.ListSettlements(ctx, c.GetString(userIDContextKey), tripID, limit, offset)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) createSettlement(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}
	input := decodeJSONRequest[trips.SettlementInput](c)
	if input == nil {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.CreateSettlement(ctx, c.GetString(userIDContextKey), tripID, key, *input)
	writeTripResult(c, http.StatusCreated, result, err)
}
