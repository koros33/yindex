package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	ratelimit "github.com/koros33/yindex/internal/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/koros33/yindex/internal/handlers"
)

func init() {
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file, using system env vars")
	} else {
		slog.Info("✅ .env loaded")
	}
}

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL is required")
		return
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("DB connection failed", "error", err)
		return
	}
	defer db.Close()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// ── Handlers ──────────────────────────────────────────────────────────────
	indexH := &handlers.IndexHandler{DB: db}
	stockH := &handlers.StockHandler{DB: db}

	// ── Router ────────────────────────────────────────────────────────────────
	rl := ratelimit.NewRateLimiter(60)
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
		r.Use(rl.Middleware)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type"},
	}))

	frontendFS := http.FileServer(http.Dir("./frontend"))
	r.Handle("/*", http.StripPrefix("/", frontendFS))



	// Routes
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", handlers.HealthHandler)

		r.Route("/index", func(r chi.Router) {
			r.Get("/latest", indexH.Latest)
			r.Get("/history", indexH.History) // ?period=1d|1w|1m|3m|6m|ytd|1y|all
			r.Get("/stats", indexH.Stats)
		})

		r.Route("/stocks", func(r chi.Router) {
			r.Get("/", stockH.All)
			r.Get("/{ticker}", stockH.ByTicker)
			r.Get("/{ticker}/history", stockH.History) // ?period=1m|ytd|all
			r.Get("/{ticker}/stats", stockH.Stats)
		})
	})

	slog.Info("🚀 Server running", "port", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		slog.Error("Server failed", "error", err)
	}
}