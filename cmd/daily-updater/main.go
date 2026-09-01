package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	MANSA_BASE_URL  = "https://mansaapi.com/api/v1/markets/exchanges/NSE/stocks"
	BASE_DATE       = "2026-08-31"
	BASE_INDEX      = 100.0
	REQUEST_TIMEOUT = 30 * time.Second
)

// Rebalance dates — yield weights recalculated from fundamentals
var REBALANCE_DATES = []string{
	"2026-08-31", // inception
	"2027-03-01",
	"2027-09-01",
	"2028-03-01",
	"2028-09-01",
}

// ── Mansa API response ────────────────────────────────────────────────────────

type MansaStock struct {
	Ticker string  `json:"ticker"`
	Name   string  `json:"name"`
	Price  float64 `json:"price"`
	Volume float64 `json:"volume"`
}

type MansaResponse struct {
	Success bool         `json:"success"`
	Data    []MansaStock `json:"data"`
}

// ── init ──────────────────────────────────────────────────────────────────────

func init() {
	paths := []string{".env", "../../.env", "../../../.env"}
	for _, p := range paths {
		if err := godotenv.Load(p); err == nil {
			slog.Info("✅ .env loaded", "path", p)
			return
		}
	}
	slog.Warn("No .env file found, using system env vars")
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	apiKey := os.Getenv("MANSA_API_KEY")
	if dbURL == "" || apiKey == "" {
		slog.Error("Missing DATABASE_URL or MANSA_API_KEY")
		os.Exit(1)
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("DB connection failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	slog.Info("🚀 NSE Dividend Index updater starting...")

	today := time.Now().UTC().Format("2006-01-02")

	// ── Step 1: Insert base date row (100.0) if not exists ───────────────────
	if err := insertBaseIfMissing(ctx, db); err != nil {
		slog.Error("Base date insert failed", "error", err)
		os.Exit(1)
	}

	// ── Step 2: Fetch today's prices from Mansa ──────────────────────────────
	slog.Info("📥 Fetching NSE prices from Mansa API...")
	stocks, err := fetchMansaPrices(apiKey)
	if err != nil {
		slog.Error("Mansa API fetch failed", "error", err)
		os.Exit(1)
	}
	slog.Info("✅ Fetched prices", "count", len(stocks))

	// ── Step 3: Save today's prices to DB ────────────────────────────────────
	saved, err := savePrices(ctx, db, stocks, today)
	if err != nil {
		slog.Error("Price save failed", "error", err)
		os.Exit(1)
	}
	slog.Info("💾 Prices saved", "rows", saved)

	// ── Step 4: Calculate and save index value for today ─────────────────────
	if today <= BASE_DATE {
		slog.Info("📊 Today is base date — index already set to 100")
		return
	}

	if err := calculateAndSaveIndex(ctx, db, today); err != nil {
		slog.Error("Index calculation failed", "error", err)
		os.Exit(1)
	}

	slog.Info("✅ Daily update complete", "date", today)
}

// ── insertBaseIfMissing ───────────────────────────────────────────────────────

func insertBaseIfMissing(ctx context.Context, db *pgxpool.Pool) error {
	var count int
	err := db.QueryRow(ctx,
		"SELECT COUNT(*) FROM index_values WHERE price_date = $1", BASE_DATE,
	).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil // already exists
	}

	_, err = db.Exec(ctx, `
		INSERT INTO index_values (price_date, index_value, daily_change)
		VALUES ($1, $2, 0)
		ON CONFLICT (price_date) DO NOTHING
	`, BASE_DATE, BASE_INDEX)
	if err != nil {
		return err
	}
	slog.Info("📌 Base date row inserted", "date", BASE_DATE, "value", BASE_INDEX)
	return nil
}

// ── fetchMansaPrices ──────────────────────────────────────────────────────────

func fetchMansaPrices(apiKey string) ([]MansaStock, error) {
	client := &http.Client{Timeout: REQUEST_TIMEOUT}

	req, err := http.NewRequest("GET", MANSA_BASE_URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mansa API %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result MansaResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if !result.Success {
		return nil, fmt.Errorf("mansa API returned success=false")
	}

	return result.Data, nil
}

// ── savePrices ────────────────────────────────────────────────────────────────

func savePrices(ctx context.Context, db *pgxpool.Pool, stocks []MansaStock, date string) (int, error) {
	saved := 0
	for _, s := range stocks {
		if s.Price <= 0 {
			continue
		}

		tag, err := db.Exec(ctx, `
			INSERT INTO prices (stock_id, price_date, close_price, volume)
			SELECT id, $2, $3, $4
			FROM stocks
			WHERE ticker = $1 AND active = TRUE
			ON CONFLICT (stock_id, price_date) DO UPDATE
				SET close_price = EXCLUDED.close_price,
				    volume = EXCLUDED.volume
		`, s.Ticker, date, s.Price, int64(s.Volume))
		if err != nil {
			slog.Warn("Price save failed", "ticker", s.Ticker, "error", err)
			continue
		}
		if tag.RowsAffected() > 0 {
			saved++
		}
	}
	return saved, nil
}

// ── calculateAndSaveIndex ─────────────────────────────────────────────────────

func calculateAndSaveIndex(ctx context.Context, db *pgxpool.Pool, today string) error {
	// Get yield weights from most recent fundamentals
	weights, err := getYieldWeights(ctx, db)
	if err != nil {
		return fmt.Errorf("yield weights: %w", err)
	}
	if len(weights) == 0 {
		return fmt.Errorf("no fundamentals found — run INSERT fundamentals first")
	}

	// Get base prices (prices on BASE_DATE)
	basePrices, err := getPricesOnDate(ctx, db, BASE_DATE)
	if err != nil {
		return fmt.Errorf("base prices: %w", err)
	}

	// Get today's prices
	todayPrices, err := getPricesOnDate(ctx, db, today)
	if err != nil {
		return fmt.Errorf("today prices: %w", err)
	}

	if len(todayPrices) == 0 {
		slog.Warn("No prices for today yet — skipping index calc", "date", today)
		return nil
	}

	// Calculate index value
	// Index(t) = 100 × Σ(yield_weight_i × price_i(t) / price_i(BASE_DATE))
	var indexValue float64
	var totalWeightUsed float64
	var missing []string

	for ticker, weight := range weights {
		basePrice, hasBase := basePrices[ticker]
		todayPrice, hasToday := todayPrices[ticker]

		if !hasBase || basePrice == 0 {
			missing = append(missing, ticker+"(no base)")
			continue
		}
		if !hasToday || todayPrice == 0 {
			missing = append(missing, ticker+"(no today)")
			continue
		}

		priceRelative := todayPrice / basePrice
		indexValue += weight * priceRelative
		totalWeightUsed += weight
	}

	if totalWeightUsed == 0 {
		return fmt.Errorf("no valid stocks for index calculation")
	}

	if len(missing) > 0 {
		slog.Warn("Some stocks skipped", "tickers", missing)
	}

	// Renormalize if any stocks were skipped
	if totalWeightUsed < 1.0 {
		indexValue = indexValue / totalWeightUsed
	}

	indexValue *= BASE_INDEX

	// Get previous index value for daily change
	var prevValue float64
	err = db.QueryRow(ctx, `
		SELECT index_value FROM index_values
		WHERE price_date < $1
		ORDER BY price_date DESC
		LIMIT 1
	`, today).Scan(&prevValue)
	if err != nil {
		prevValue = BASE_INDEX
	}
	dailyChange := indexValue - prevValue

	// Save to index_values
	_, err = db.Exec(ctx, `
		INSERT INTO index_values (price_date, index_value, daily_change)
		VALUES ($1, $2, $3)
		ON CONFLICT (price_date) DO UPDATE
			SET index_value = EXCLUDED.index_value,
			    daily_change = EXCLUDED.daily_change
	`, today, indexValue, dailyChange)
	if err != nil {
		return fmt.Errorf("index save: %w", err)
	}

	slog.Info("📊 Index calculated",
		"date", today,
		"value", fmt.Sprintf("%.4f", indexValue),
		"change", fmt.Sprintf("%+.4f", dailyChange),
		"stocks_used", len(weights)-len(missing),
	)
	return nil
}

// ── getYieldWeights ───────────────────────────────────────────────────────────

func getYieldWeights(ctx context.Context, db *pgxpool.Pool) (map[string]float64, error) {
	// Get most recent fundamentals per stock
	rows, err := db.Query(ctx, `
		SELECT s.ticker, f.dividend_yield_ttm
		FROM fundamentals f
		JOIN stocks s ON s.id = f.stock_id
		WHERE s.active = TRUE
		  AND f.as_of_date = (
			SELECT MAX(as_of_date) FROM fundamentals f2
			WHERE f2.stock_id = f.stock_id
		  )
		  AND f.dividend_yield_ttm > 0
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	yields := make(map[string]float64)
	var totalYield float64

	for rows.Next() {
		var ticker string
		var yield float64
		if err := rows.Scan(&ticker, &yield); err != nil {
			continue
		}
		yields[ticker] = yield
		totalYield += yield
	}

	if totalYield == 0 {
		return nil, fmt.Errorf("total yield is zero")
	}

	// Normalize to weights
	weights := make(map[string]float64)
	for ticker, yield := range yields {
		weights[ticker] = yield / totalYield
	}

	// Log weight table
	type tw struct{ ticker string; weight float64 }
	var sorted []tw
	for t, w := range weights {
		sorted = append(sorted, tw{t, w})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].weight > sorted[j].weight })
	for _, s := range sorted {
		slog.Info("⚖️  Weight", "ticker", s.ticker, "weight_pct", fmt.Sprintf("%.3f%%", s.weight*100))
	}

	return weights, nil
}

// ── getPricesOnDate ───────────────────────────────────────────────────────────

func getPricesOnDate(ctx context.Context, db *pgxpool.Pool, date string) (map[string]float64, error) {
	rows, err := db.Query(ctx, `
		SELECT s.ticker, p.close_price
		FROM prices p
		JOIN stocks s ON s.id = p.stock_id
		WHERE p.price_date = $1 AND s.active = TRUE
	`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	prices := make(map[string]float64)
	for rows.Next() {
		var ticker string
		var price float64
		if err := rows.Scan(&ticker, &price); err != nil {
			continue
		}
		prices[ticker] = price
	}
	return prices, nil
}