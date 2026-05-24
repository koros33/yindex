/**package main

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

func init() {
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file found - using system environment variables")
	}
}

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	polygonKey := os.Getenv("POLYGON_API_KEY")

	if dbURL == "" || polygonKey == "" {
		slog.Error("Missing DATABASE_URL or POLYGON_API_KEY")
		return
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("Failed to connect to database", "error", err)
		return
	}
	defer db.Close()

	slog.Info("🚀 Starting hourly index updater...")

	lastDate := getLastSavedDate(ctx, db)
	startDate := lastDate.AddDate(0, 0, 1) // Start from next day

	slog.Info("Last saved date", "date", lastDate.Format("2006-01-02"))
	slog.Info("Fetching new data from", "date", startDate.Format("2006-01-02"))

	updateAllStocksPrices(ctx, db, polygonKey, startDate)
	recalculateIndex(ctx, db)

	slog.Info("✅ Hourly updater finished successfully!")
}

func getLastSavedDate(ctx context.Context, db *pgxpool.Pool) time.Time {
	var lastDate time.Time
	err := db.QueryRow(ctx, "SELECT MAX(price_date) FROM index_values").Scan(&lastDate)
	if err != nil || lastDate.IsZero() {
		return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return lastDate
}

type AggResponse struct {
	Results []struct {
		Timestamp int64   `json:"t"`
		Close     float64 `json:"c"`
	} `json:"results"`
}

func updateAllStocksPrices(ctx context.Context, db *pgxpool.Pool, apiKey string, startDate time.Time) {
	var stocks []struct {
		ID     int
		Ticker string
	}

	rows, _ := db.Query(ctx, "SELECT id, ticker FROM stocks WHERE active = TRUE")
	defer rows.Close()

	for rows.Next() {
		var s struct{ ID int; Ticker string }
		rows.Scan(&s.ID, &s.Ticker)
		stocks = append(stocks, s)
	}

	endDate := time.Now().Format("2006-01-02")

	for _, s := range stocks {
		fetchAndSaveStock(ctx, db, apiKey, s.ID, s.Ticker, startDate.Format("2006-01-02"), endDate)
		time.Sleep(13 * time.Second) // Safe for free tier
	}
}

func fetchAndSaveStock(ctx context.Context, db *pgxpool.Pool, apiKey string, stockID int, ticker, from, to string) {
	url := fmt.Sprintf("https://api.polygon.io/v2/aggs/ticker/%s/range/1/day/%s/%s?adjusted=true&sort=asc&apiKey=%s",
		ticker, from, to, apiKey)

	resp, err := http.Get(url)
	if err != nil {
		slog.Error("HTTP request failed", "ticker", ticker, "error", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var data AggResponse
	if err := json.Unmarshal(body, &data); err != nil {
		slog.Error("JSON unmarshal failed", "ticker", ticker, "error", err)
		return
	}

	for _, a := range data.Results {
		dateStr := time.UnixMilli(a.Timestamp).UTC().Format("2006-01-02")

		_, err = db.Exec(ctx, `
			INSERT INTO prices (stock_id, price_date, close_price)
			VALUES ($1, $2, $3)
			ON CONFLICT (stock_id, price_date) 
			DO UPDATE SET close_price = EXCLUDED.close_price`,
			stockID, dateStr, a.Close)
		if err != nil {
			slog.Warn("DB insert warning", "ticker", ticker, "error", err)
		}
	}

	slog.Info("✓ Updated", "ticker", ticker, "bars", len(data.Results))
}

func recalculateIndex(ctx context.Context, db *pgxpool.Pool) {
	_, err := db.Exec(ctx, `
		WITH normalized AS (
			SELECT 
				p.stock_id,
				p.price_date,
				p.close_price,
				FIRST_VALUE(p.close_price) OVER (PARTITION BY p.stock_id, date_trunc('quarter', p.price_date) ORDER BY p.price_date) as base_price
			FROM prices p
			JOIN stocks s ON s.id = p.stock_id
			WHERE s.active = TRUE
		)
		INSERT INTO index_values (price_date, index_value, daily_change)
		SELECT 
			price_date,
			AVG(close_price / base_price) * 100 as index_value,
			(AVG(close_price / base_price) * 100 - LAG(AVG(close_price / base_price) * 100) OVER (ORDER BY price_date)) as daily_change
		FROM normalized
		GROUP BY price_date
		ON CONFLICT (price_date) 
		DO UPDATE SET 
			index_value = EXCLUDED.index_value,
			daily_change = EXCLUDED.daily_change;
	`)
	if err != nil {
		slog.Error("Index recalculation failed", "error", err)
	} else {
		slog.Info("✅ Index recalculated successfully")
	}
}**/
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

func init() {
	_ = godotenv.Load()
	slog.Info("Attempted to load .env")
}

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	polygonKey := os.Getenv("POLYGON_API_KEY")

	if dbURL == "" || polygonKey == "" {
		slog.Error("Missing environment variables")
		return
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("DB connection failed", "error", err)
		return
	}
	defer db.Close()

	slog.Info("🚀 Starting hourly index updater...")

	lastDate := getLastSavedDate(ctx, db)
	startDate := lastDate.AddDate(0, 0, 1)

	slog.Info("Last data", "date", lastDate.Format("2006-01-02"))
	slog.Info("Fetching from", "date", startDate.Format("2006-01-02"))

	updateAllStocksPrices(ctx, db, polygonKey, startDate)
	recalculateIndex(ctx, db)

	slog.Info("✅ Updater completed successfully!")
}

// === Rest of functions ===
func getLastSavedDate(ctx context.Context, db *pgxpool.Pool) time.Time {
	var lastDate time.Time
	err := db.QueryRow(ctx, "SELECT MAX(price_date) FROM index_values").Scan(&lastDate)
	if err != nil || lastDate.IsZero() {
		return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return lastDate
}

type AggResponse struct {
	Results []struct {
		Timestamp int64   `json:"t"`
		Close     float64 `json:"c"`
	} `json:"results"`
}

func updateAllStocksPrices(ctx context.Context, db *pgxpool.Pool, apiKey string, startDate time.Time) {
	var stocks []struct {
		ID     int
		Ticker string
	}

	rows, _ := db.Query(ctx, "SELECT id, ticker FROM stocks WHERE active = TRUE")
	defer rows.Close()

	for rows.Next() {
		var s struct{ ID int; Ticker string }
		rows.Scan(&s.ID, &s.Ticker)
		stocks = append(stocks, s)
	}

	endDate := time.Now().Format("2006-01-02")

	for _, s := range stocks {
		fetchAndSaveStock(ctx, db, apiKey, s.ID, s.Ticker, startDate.Format("2006-01-02"), endDate)
		time.Sleep(13 * time.Second)
	}
}

func fetchAndSaveStock(ctx context.Context, db *pgxpool.Pool, apiKey string, stockID int, ticker, from, to string) {
	url := fmt.Sprintf("https://api.polygon.io/v2/aggs/ticker/%s/range/1/day/%s/%s?adjusted=true&sort=asc&apiKey=%s",
		ticker, from, to, apiKey)

	resp, err := http.Get(url)
	if err != nil {
		slog.Error("HTTP failed", "ticker", ticker, "error", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data AggResponse
	json.Unmarshal(body, &data)

	for _, a := range data.Results {
		dateStr := time.UnixMilli(a.Timestamp).UTC().Format("2006-01-02")
		_, _ = db.Exec(ctx, `
			INSERT INTO prices (stock_id, price_date, close_price)
			VALUES ($1, $2, $3)
			ON CONFLICT (stock_id, price_date) DO UPDATE SET close_price = EXCLUDED.close_price`,
			stockID, dateStr, a.Close)
	}
	slog.Info("Updated", "ticker", ticker, "bars", len(data.Results))
}

func recalculateIndex(ctx context.Context, db *pgxpool.Pool) {
	_, err := db.Exec(ctx, `
		WITH normalized AS (
			SELECT p.stock_id, p.price_date, p.close_price,
				FIRST_VALUE(p.close_price) OVER (PARTITION BY p.stock_id, date_trunc('quarter', p.price_date) ORDER BY p.price_date) as base_price
			FROM prices p JOIN stocks s ON s.id = p.stock_id WHERE s.active = TRUE
		)
		INSERT INTO index_values (price_date, index_value, daily_change)
		SELECT price_date, AVG(close_price / base_price) * 100,
			(AVG(close_price / base_price) * 100 - LAG(AVG(close_price / base_price) * 100) OVER (ORDER BY price_date))
		FROM normalized GROUP BY price_date
		ON CONFLICT (price_date) DO UPDATE SET 
			index_value = EXCLUDED.index_value,
			daily_change = EXCLUDED.daily_change;
	`)
	if err != nil {
		slog.Error("Recalc failed", "error", err)
	} else {
		slog.Info("✅ Index recalculated")
	}
}