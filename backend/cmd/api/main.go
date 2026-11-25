package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Max252t/T-Travels/backend/api"
	"github.com/Max252t/T-Travels/backend/internal/auth"
	"github.com/Max252t/T-Travels/backend/internal/trips"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("HTTP server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	jwtKey := []byte(os.Getenv("JWT_SECRET"))
	if len(jwtKey) < 32 {
		return errors.New("JWT_SECRET must be at least 32 bytes")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("invalid PostgreSQL configuration")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("could not connect to PostgreSQL")
	}

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           newRouter(auth.NewService(pool, jwtKey), trips.NewService(pool)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
	}
	slog.Info("starting HTTP server", "addr", addr)
	return server.ListenAndServe()
}

func newRouter(authService *auth.Service, tripService *trips.Service) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml", []byte(api.Swagger))
	})
	authRoutes := router.Group("/api/v1/auth")
	authRoutes.POST("/register", registerUser(authService))
	authRoutes.POST("/login", loginUser(authService))
	authRoutes.POST("/refresh", refreshSession(authService))
	authRoutes.POST("/logout", logoutSession(authService))
	users := router.Group("/api/v1/users", requireAccessToken(authService))
	users.GET("/me", getCurrentUser(authService))
	registerTripRoutes(router.Group("/api/v1", requireAccessToken(authService)), tripService)
	return router
}
