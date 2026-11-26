package main

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Max252t/T-Travels/backend/internal/trips"
	"github.com/gin-gonic/gin"
)

const snapshotRevisionHeader = "X-Snapshot-Revision"

func expectedRevision(c *gin.Context, offset int32) (int64, bool) {
	values, exists := c.GetQueryArray("revision")
	if !exists {
		if offset == 0 {
			return 0, true
		}
		writeAPIError(c, http.StatusBadRequest, "SNAPSHOT_REVISION_REQUIRED", "Для продолжения передайте ревизию первой страницы")
		return 0, false
	}
	if len(values) == 1 {
		revision, err := strconv.ParseInt(values[0], 10, 64)
		if err == nil && revision > 0 {
			return revision, true
		}
	}
	writeAPIError(c, http.StatusBadRequest, "INVALID_SNAPSHOT_REVISION", "Ревизия должна быть положительным целым числом")
	return 0, false
}

func writeRevisioned[T any](c *gin.Context, result trips.Revisioned[T], err error) {
	if err == nil || errors.Is(err, trips.ErrSnapshotRevision) {
		setSnapshotRevision(c, result.Revision)
	}
	writeTripResult(c, http.StatusOK, result.Value, err)
}

func setSnapshotRevision(c *gin.Context, revision int64) {
	c.Header(snapshotRevisionHeader, strconv.FormatInt(revision, 10))
}
