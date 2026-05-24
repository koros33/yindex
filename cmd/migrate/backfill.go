/**package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/polygon-io/client-go/rest"
	"github.com/polygon-io/client-go/rest/models"
)

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	polygonKey := os.Getenv("POLYGON_API_KEY")

	if dbURL == "" || polygonKey == "" {
		slog.Error("Missing environment variables", "DATABASE_URL", dbURL != "", "POLYGON_API_KEY", polygonKey != "")
		return
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("Failed to connect to database", "error", err)
		return
	}
	defer db.Close()

	client := rest.NewClient(polygonKey)

	slog.Info("Starting Polygon migration to 20 stocks...")

	// Step 1: Backfill prices
	stocks := getActiveStocks(ctx, db)
	for _, s := range stocks {
		backfillStockPrices(ctx, db, client, s.ID, s.Ticker)
		time.Sleep(13 * time.Second) // Safe for free tier
	}
**/
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
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file found, using system env vars")
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

	slog.Info("🚀 Starting simple Polygon backfill...")

	stocks := getActiveStocks(ctx, db)

	for _, s := range stocks {
		fetchAndSavePrices(ctx, db, apiKey, s.ID, s.Ticker)
		time.Sleep(13 * time.Second) // Free tier
	}

	slog.Info("Recalculating index...")
	recalculateIndex(ctx, db)

	slog.Info("🎉 Migration finished!")
}

type Stock struct {
	ID     int
	Ticker string
}

func getActiveStocks(ctx context.Context, db *pgxpool.Pool) []Stock {
	rows, _ := db.Query(ctx, "SELECT id, ticker FROM stocks WHERE active = TRUE")
	defer rows.Close()

	var stocks []Stock
	for rows.Next() {
		var s Stock
		rows.Scan(&s.ID, &s.Ticker)
		stocks = append(stocks, s)
	}
	return stocks
}

type AggResponse struct {
	Results []struct {
		Timestamp int64   `json:"t"`
		Close     float64 `json:"c"`
	} `json:"results"`
}

func fetchAndSavePrices(ctx context.Context, db *pgxpool.Pool, apiKey string, stockID int, ticker string) {
	slog.Info("Fetching", "ticker", ticker)

	url := fmt.Sprintf("https://api.polygon.io/v2/aggs/ticker/%s/range/1/day/2025-01-01/%s?adjusted=true&sort=asc&apiKey=%s",
		ticker, time.Now().Format("2006-01-02"), apiKey)

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

	for _, a := range data.Results {
		dateStr := time.UnixMilli(a.Timestamp).UTC().Format("2006-01-02")

		_, _ = db.Exec(ctx, `
			INSERT INTO prices (stock_id, price_date, close_price)
			VALUES ($1, $2, $3)
			ON CONFLICT (stock_id, price_date) 
			DO UPDATE SET close_price = EXCLUDED.close_price`,
			stockID, dateStr, a.Close)
	}

	slog.Info("✓ Saved", "ticker", ticker, "bars", len(data.Results))
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
			AVG(close_price / base_price) * 100,
			(AVG(close_price / base_price) * 100 - LAG(AVG(close_price / base_price) * 100) OVER (ORDER BY price_date))
		FROM normalized
		GROUP BY price_date
		ON CONFLICT (price_date) DO UPDATE 
		SET index_value = EXCLUDED.index_value,
		    daily_change = EXCLUDED.daily_change;
	`)
	if err != nil {
		slog.Error("Index recalc failed", "error", err)
	} else {
		slog.Info("✅ Index recalculated successfully")
	}
}