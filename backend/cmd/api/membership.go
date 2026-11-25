package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h tripRoutes) leaveTrip(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok || !noBody(c) {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	err := h.service.Leave(ctx, c.GetString(userIDContextKey), tripID, key)
	writeTripResult(c, http.StatusNoContent, nil, err)
}

func (h tripRoutes) removeMember(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	targetID, ok := resourceID(c, "userId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok || !noBody(c) {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	err := h.service.RemoveMember(ctx, c.GetString(userIDContextKey), tripID, targetID, key)
	writeTripResult(c, http.StatusNoContent, nil, err)
}
