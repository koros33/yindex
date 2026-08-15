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

// BASE_DATE is the fixed anchor for the index (first trading day of 2026)
const BASE_DATE = "2024-06-07"

func init() {
    possiblePaths := []string{
        ".env",
        "../.env",
        "../../.env",
        "/workspaces/Nse-pulse/.env",     // most likely
        "/workspaces/Nse-pulse/cmd/migrate/.env",
    }

    loaded := false
    for _, path := range possiblePaths {
        if err := godotenv.Load(path); err == nil {
            slog.Info("✅ Loaded .env successfully", "path", path)
            loaded = true
            break
        }
    }

    if !loaded {
        slog.Error("❌ Could not find .env in any common location")
    
    }
}
func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	apiKey := os.Getenv("POLYGON_API_KEY")
	if dbURL == "" || apiKey == "" {
		slog.Error("Set DATABASE_URL and POLYGON_API_KEY")
		return
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("DB failed", "error", err)
		return
	}
	defer db.Close()

	slog.Info("🚀 Starting backfil7l from base date...", "base_date", BASE_DATE)

	stocks := getActiveStocks(ctx, db)
	slog.Info("Loaded active stocks", "count", len(stocks))

	for i, s := range stocks {
		slog.Info(fmt.Sprintf("(%d/%d) Fetching", i+1, len(stocks)), "ticker", s.Ticker)
		fetchAndSavePrices(ctx, db, apiKey, s.ID, s.Ticker)
		if i < len(stocks)-1 {
			time.Sleep(13 * time.Second) // Polygon free tier: ~5 req/min
		}
	}

	slog.Info("📊 Recalculating index...")
	recalculateIndex(ctx, db)
	slog.Info("🎉 Backfill finished!")
}

type Stock struct {
	ID     int
	Ticker string
}

func getActiveStocks(ctx context.Context, db *pgxpool.Pool) []Stock {
	rows, err := db.Query(ctx, "SELECT id, ticker FROM stocks WHERE active = TRUE ORDER BY ticker")
	if err != nil {
		slog.Error("Failed to query stocks", "error", err)
		return nil
	}
	defer rows.Close()

	var stocks []Stock
	for rows.Next() {
		var s Stock
		if err := rows.Scan(&s.ID, &s.Ticker); err != nil {
			slog.Warn("Failed to scan stock row", "error", err)
			continue
		}
		stocks = append(stocks, s)
	}
	return stocks
}

type AggResponse struct {
	Results []struct {
		Timestamp int64   `json:"t"`
		Close     float64 `json:"c"`
	} `json:"results"`
	Status  string `json:"status"`
	Count   int    `json:"resultsCount"`
}

func fetchAndSavePrices(ctx context.Context, db *pgxpool.Pool, apiKey string, stockID int, ticker string) {
	today := time.Now().Format("2006-01-02")

	// Only fetch from base date onwards — no 2025 data
	url := fmt.Sprintf(
		"https://api.polygon.io/v2/aggs/ticker/%s/range/1/day/%s/%s?adjusted=true&sort=asc&limit=50000&apiKey=%s",
		ticker, BASE_DATE, today, apiKey,
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
		slog.Warn("No data returned", "ticker", ticker, "status", data.Status)
		return
	}

	saved := 0
	for _, a := range data.Results {
		dateStr := time.UnixMilli(a.Timestamp).UTC().Format("2006-01-02")

		// Skip anything before base date (safety guard)
		if dateStr < BASE_DATE {
			continue
		}

		_, err := db.Exec(ctx, `
			INSERT INTO prices (stock_id, price_date, close_price)
			VALUES ($1, $2, $3)
			ON CONFLICT (stock_id, price_date)
			DO UPDATE SET close_price = EXCLUDED.close_price`,
			stockID, dateStr, a.Close,
		)
		if err != nil {
			slog.Warn("Insert failed", "ticker", ticker, "date", dateStr, "error", err)
		} else {
			saved++
		}
	}

	slog.Info("✓ Saved", "ticker", ticker, "bars", saved)
}

func recalculateIndex(ctx context.Context, db *pgxpool.Pool) {
	// Fixed base date anchor: index = 100 on BASE_DATE
	// Formula: Index(t) = 100 × mean( P(t,i) / P(base,i) ) across all active stocks
	_, err := db.Exec(ctx, `
		WITH base_prices AS (
			-- Get the closing price for each stock on the base date
			SELECT p.stock_id, p.close_price AS base_price
			FROM prices p
			JOIN stocks s ON s.id = p.stock_id
			WHERE p.price_date = $1
			  AND s.active = TRUE
		),
		normalized AS (
			-- Compute each stock's return relative to its base price
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
			-- Equal-weight average of all relative returns × 100
			SELECT
				price_date,
				AVG(relative_return) * 100 AS index_value
			FROM normalized
			GROUP BY price_date
			HAVING COUNT(*) = (SELECT COUNT(*) FROM stocks WHERE active = TRUE)
			-- Only include dates where ALL 100 stocks have data
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
		slog.Error("Index recalc failed", "error", err)
	} else {
		slog.Info("✅ Index recalculated with fixed base date", "base_date", BASE_DATE)
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

const BASE_DATE = "2024-06-14"
const MIN_BARS = 400 // 2 years ≈ 500 trading days, 400 = already backfilled

func init() {
	for _, path := range []string{".env", "../.env", "../../.env", "/workspaces/Nse-pulse/.env"} {
		if err := godotenv.Load(path); err == nil {
			slog.Info("✅ .env loaded", "path", path)
			return
		}
	}
	slog.Warn("No .env file found, using system env vars")
}

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	apiKey := os.Getenv("POLYGON_API_KEY")
	if dbURL == "" || apiKey == "" {
		slog.Error("Set DATABASE_URL and POLYGON_API_KEY")
		return
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("DB failed", "error", err)
		return
	}
	defer db.Close()

	stocks := getActiveStocks(ctx, db)
	slog.Info("🚀 Starting backfill", "base_date", BASE_DATE, "total_stocks", len(stocks))

	skipped := 0
	fetched := 0

	for i, s := range stocks {
		if alreadyBackfilled(ctx, db, s.ID) {
			slog.Info(fmt.Sprintf("(%d/%d) ⏭ Skipping", i+1, len(stocks)), "ticker", s.Ticker)
			skipped++
			continue
		}

		slog.Info(fmt.Sprintf("(%d/%d) Fetching", i+1, len(stocks)), "ticker", s.Ticker)
		fetchAndSavePrices(ctx, db, apiKey, s.ID, s.Ticker)
		fetched++

		// Only sleep between actual API calls not skips
		if i < len(stocks)-1 {
			time.Sleep(13 * time.Second)
		}
	}

	slog.Info("📊 Fetch complete", "skipped", skipped, "fetched", fetched)
	
	slog.Info("🎉 Backfill finished!")
}

// alreadyBackfilled returns true if stock has enough historical data
func alreadyBackfilled(ctx context.Context, db *pgxpool.Pool, stockID int) bool {
	var count int
	err := db.QueryRow(ctx, `
		SELECT COUNT(*) FROM prices
		WHERE stock_id = $1 AND price_date >= $2
	`, stockID, BASE_DATE).Scan(&count)
	if err != nil {
		return false
	}
	return count >= MIN_BARS
}

type Stock struct {
	ID     int
	Ticker string
}

func getActiveStocks(ctx context.Context, db *pgxpool.Pool) []Stock {
	rows, err := db.Query(ctx, "SELECT id, ticker FROM stocks WHERE active = TRUE ORDER BY ticker")
	if err != nil {
		slog.Error("Failed to query stocks", "error", err)
		return nil
	}
	defer rows.Close()

	var stocks []Stock
	for rows.Next() {
		var s Stock
		if err := rows.Scan(&s.ID, &s.Ticker); err != nil {
			slog.Warn("Failed to scan stock row", "error", err)
			continue
		}
		stocks = append(stocks, s)
	}
	return stocks
}

type AggResponse struct {
	Results []struct {
		Timestamp int64   `json:"t"`
		Close     float64 `json:"c"`
	} `json:"results"`
	Status string `json:"status"`
	Count  int    `json:"resultsCount"`
}

func fetchAndSavePrices(ctx context.Context, db *pgxpool.Pool, apiKey string, stockID int, ticker string) {
	today := time.Now().Format("2006-01-02")

	url := fmt.Sprintf(
		"https://api.polygon.io/v2/aggs/ticker/%s/range/1/day/%s/%s?adjusted=true&sort=asc&limit=50000&apiKey=%s",
		ticker, BASE_DATE, today, apiKey,
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
		slog.Warn("No data returned", "ticker", ticker, "status", data.Status)
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
			stockID, dateStr, a.Close,
		)
		if err != nil {
			slog.Warn("Insert failed", "ticker", ticker, "date", dateStr, "error", err)
		} else {
			saved++
		}
	}
	slog.Info("✓ Saved", "ticker", ticker, "bars", saved)
}
