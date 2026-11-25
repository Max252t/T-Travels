package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h tripRoutes) notifications(c *gin.Context) {
	limit, offset, ok := page(c)
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Notifications(ctx, c.GetString(userIDContextKey), limit, offset)
	writeTripResult(c, http.StatusOK, result, err)
}

func (h tripRoutes) readNotification(c *gin.Context) {
	id, ok := resourceID(c, "notificationId")
	if !ok || !noBody(c) {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	err := h.service.ReadNotification(ctx, c.GetString(userIDContextKey), id)
	writeTripResult(c, http.StatusNoContent, nil, err)
}

func (h tripRoutes) readAllNotifications(c *gin.Context) {
	key, ok := idempotencyKey(c)
	if !ok || !noBody(c) {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	err := h.service.ReadAllNotifications(ctx, c.GetString(userIDContextKey), key)
	writeTripResult(c, http.StatusNoContent, nil, err)
}
