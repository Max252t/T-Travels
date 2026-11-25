package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/trips"
	"github.com/gin-gonic/gin"
)

func (h tripRoutes) getReceipt(c *gin.Context) {
	tripID, expenseID, ok := expenseIDs(c)
	if !ok || !h.acquireReceiptSlot(c) {
		return
	}
	defer func() { <-h.receiptSlots }()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	metadata, data, err := h.service.GetReceipt(ctx, c.GetString(userIDContextKey), tripID, expenseID)
	if err != nil {
		writeTripResult(c, 0, nil, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, metadata.ContentType, data)
}

func (h tripRoutes) putReceipt(c *gin.Context) {
	tripID, expenseID, ok := expenseIDs(c)
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok || !h.acquireReceiptSlot(c) {
		return
	}
	defer func() { <-h.receiptSlots }()
	data, contentType, ok := readReceipt(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	metadata, err := h.service.PutReceipt(ctx, c.GetString(userIDContextKey), tripID, expenseID, key, contentType, data)
	writeTripResult(c, http.StatusCreated, metadata, err)
}

func (h tripRoutes) deleteReceipt(c *gin.Context) {
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
	err := h.service.DeleteReceipt(ctx, c.GetString(userIDContextKey), tripID, expenseID, key)
	writeTripResult(c, http.StatusNoContent, nil, err)
}

func (h tripRoutes) acquireReceiptSlot(c *gin.Context) bool {
	select {
	case h.receiptSlots <- struct{}{}:
		return true
	default:
		c.Header("Retry-After", "1")
		writeAPIError(c, http.StatusTooManyRequests, "RECEIPT_BUSY", "Повторите запрос чека позже")
		return false
	}
}

func readReceipt(c *gin.Context) ([]byte, string, bool) {
	media, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	encodings := c.Request.Header.Values("Content-Encoding")
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" ||
		len(encodings) > 1 || (len(encodings) == 1 && !strings.EqualFold(encodings[0], "identity")) {
		writeAPIError(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_RECEIPT_MEDIA", "Отправьте один файл JPEG или PNG в multipart/form-data")
		return nil, "", false
	}
	const limit = trips.MaxReceiptSize + (16 << 10)
	if c.Request.ContentLength > limit {
		writeAPIError(c, http.StatusRequestEntityTooLarge, "RECEIPT_TOO_LARGE", "Размер файла превышает 5 МиБ")
		return nil, "", false
	}
	reader := multipart.NewReader(http.MaxBytesReader(c.Writer, c.Request.Body, limit), params["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		receiptReadError(c, err)
		return nil, "", false
	}
	if part.FormName() != "file" || part.FileName() == "" {
		receiptReadError(c, nil)
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(part, trips.MaxReceiptSize+1))
	if len(data) > trips.MaxReceiptSize {
		writeAPIError(c, http.StatusRequestEntityTooLarge, "RECEIPT_TOO_LARGE", "Размер файла превышает 5 МиБ")
		return nil, "", false
	}
	if err != nil {
		receiptReadError(c, err)
		return nil, "", false
	}
	if _, err := reader.NextPart(); err != io.EOF {
		receiptReadError(c, err)
		return nil, "", false
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || config.Width < 1 || config.Height < 1 ||
		config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 12_000_000 {
		writeAPIError(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_RECEIPT_MEDIA", "Нужен JPEG или PNG до 4096 пикселей на сторону и 12 мегапикселей")
		return nil, "", false
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		writeAPIError(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_RECEIPT_MEDIA", "Файл изображения повреждён")
		return nil, "", false
	}
	return data, "image/" + format, true
}

func receiptReadError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeAPIError(c, http.StatusRequestEntityTooLarge, "RECEIPT_TOO_LARGE", "Размер файла превышает 5 МиБ")
		return
	}
	writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "Передайте один файл в поле file")
}

func (h tripRoutes) syncTrip(c *gin.Context) {
	tripID, ok := resourceID(c, "tripId")
	if !ok {
		return
	}
	ctx, cancel := tripContext(c)
	defer cancel()
	result, err := h.service.Snapshot(ctx, c.GetString(userIDContextKey), tripID)
	writeTripResult(c, http.StatusOK, result, err)
}
