package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Max252t/T-Travels/backend/api"
	"github.com/gin-gonic/gin"
)

func main() {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	slog.Info("starting HTTP server", "addr", addr)
	if err := server.ListenAndServe(); err != nil {
		slog.Error("HTTP server stopped", "error", err)
		os.Exit(1)
	}
}

func newRouter() *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml", []byte(api.Swagger))
	})
	return router
}
