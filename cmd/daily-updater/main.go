package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

const BASE_DATE = "2026-01-02"

func init() {
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file found, using system env vars")
	} else {
		slog.Info("✅ .env loaded")
	}
}

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	apiKey := os.Getenv("POLYGON_API_KEY")

	if dbURL == "" || apiKey == "" {
		slog.Error("Missing DATABASE_URL or POLYGON_API_KEY")
		return
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("DB connection failed", "error", err)
		return
	}
	defer db.Close()

	slog.Info("🚀 Starting daily index updater...")

	lastDate := getLastSavedDate(ctx, db)
	startDate := lastDate.AddDate(0, 0, 1)

	slog.Info("Last saved date", "date", lastDate.Format("2006-01-02"))
	slog.Info("Fetching data from", "date", startDate.Format("2006-01-02"))

	updateAllStocksPrices(ctx, db, apiKey, startDate)
	recalculateIndex(ctx, db)

	slog.Info("✅ Daily updater finished successfully!")
}

func getLastSavedDate(ctx context.Context, db *pgxpool.Pool) time.Time {
	var lastDate time.Time
	err := db.QueryRow(ctx, "SELECT MAX(price_date) FROM prices").Scan(&lastDate)
	if err != nil || lastDate.IsZero() {
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // Changed to 2026
	}
	return lastDate
}

// ==================== FETCH PRICES ====================

type AggResponse struct {
	Results []struct {
		Timestamp int64   `json:"t"`
		Close     float64 `json:"c"`
	} `json:"results"`
	Status string `json:"status"`
}

func updateAllStocksPrices(ctx context.Context, db *pgxpool.Pool, apiKey string, startDate time.Time) {
	rows, err := db.Query(ctx, "SELECT id, ticker FROM stocks WHERE active = TRUE")
	if err != nil {
		slog.Error("Failed to fetch stocks", "error", err)
		return
	}
	defer rows.Close()

	var stocks []struct{ ID int; Ticker string }
	for rows.Next() {
		var s struct{ ID int; Ticker string }
		if err := rows.Scan(&s.ID, &s.Ticker); err != nil {
			continue
		}
		stocks = append(stocks, s)
	}

	endDate := time.Now().Format("2006-01-02")

	for i, s := range stocks {
		fetchAndSaveStock(ctx, db, apiKey, s.ID, s.Ticker, startDate.Format("2006-01-02"), endDate)
		
		// Rate limiting
		if i < len(stocks)-1 {
			time.Sleep(12 * time.Second) // Slightly faster but safe
		}
	}
}

func fetchAndSaveStock(ctx context.Context, db *pgxpool.Pool, apiKey string, stockID int, ticker, from, to string) {
	url := fmt.Sprintf(
		"https://api.polygon.io/v2/aggs/ticker/%s/range/1/day/%s/%s?adjusted=true&sort=asc&apiKey=%s",
		ticker, from, to, apiKey,
	)

	resp, err := http.Get(url)
	if err != nil {
		slog.Error("HTTP failed", "ticker", ticker, "error", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var data AggResponse
	if err := json.Unmarshal(body, &data); err != nil {
		slog.Error("JSON parse failed", "ticker", ticker, "error", err)
		return
	}

	if data.Status == "ERROR" || len(data.Results) == 0 {
		slog.Warn("No data", "ticker", ticker)
		return
	}

	saved := 0
	for _, a := range data.Results {
		dateStr := time.UnixMilli(a.Timestamp).UTC().Format("2006-01-02")
		if dateStr < BASE_DATE {
			continue
		}

		_, err := db.Exec(ctx, `
			INSERT INTO prices (stock_id, price_date, close_price)
			VALUES ($1, $2, $3)
			ON CONFLICT (stock_id, price_date) 
			DO UPDATE SET close_price = EXCLUDED.close_price`,
			stockID, dateStr, a.Close)

		if err == nil {
			saved++
		}
	}

	slog.Info("✓ Updated", "ticker", ticker, "new_bars", saved)
}

// ==================== RECALCULATE INDEX (Fixed Base) ====================

func recalculateIndex(ctx context.Context, db *pgxpool.Pool) {
	_, err := db.Exec(ctx, `
		WITH base_prices AS (
			SELECT p.stock_id, p.close_price AS base_price
			FROM prices p
			WHERE p.price_date = $1
		),
		normalized AS (
			SELECT 
				p.price_date,
				p.close_price / b.base_price AS relative_return
			FROM prices p
			JOIN base_prices b ON b.stock_id = p.stock_id
			JOIN stocks s ON s.id = p.stock_id
			WHERE s.active = TRUE
			  AND p.price_date >= $1
		),
		daily_index AS (
			SELECT
				price_date,
				AVG(relative_return) * 100 AS index_value
			FROM normalized
			GROUP BY price_date
			HAVING COUNT(*) = (SELECT COUNT(*) FROM stocks WHERE active = TRUE)
		)
		INSERT INTO index_values (price_date, index_value, daily_change)
		SELECT
			price_date,
			index_value,
			index_value - LAG(index_value) OVER (ORDER BY price_date) AS daily_change
		FROM daily_index
		ORDER BY price_date
		ON CONFLICT (price_date) DO UPDATE
			SET index_value  = EXCLUDED.index_value,
			    daily_change = EXCLUDED.daily_change;
	`, BASE_DATE)

	if err != nil {
		slog.Error("Index recalculation failed", "error", err)
	} else {
		slog.Info("✅ Index recalculated with fixed base", "base_date", BASE_DATE)
	}
}