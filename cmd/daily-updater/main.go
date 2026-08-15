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
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

const BASE_DATE = "2024-06-07"

var QUARTERLY_REBALANCE_DATES = []string{
	"2024-06-07", "2024-09-07", "2024-12-07",
	"2025-03-07", "2025-06-07", "2025-09-07", "2025-12-07",
	"2026-03-07", "2026-06-07", "2026-09-07", "2026-12-07",
}

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

	lastDate, err := getLastSavedDate(ctx, db)
	if err != nil {
		slog.Error("Failed to get last saved date", "error", err)
		os.Exit(1)
	}

	// ── Backfill any gaps in existing data ──────────────────────────
	gapRows, err := db.Query(ctx, "SELECT id, ticker FROM stocks WHERE active = TRUE")
	if err != nil {
		slog.Error("Failed to query stocks for gap check", "error", err)
	} else {
		type stockRow struct {
			ID     int
			Ticker string
		}
		var allStocks []stockRow
		for gapRows.Next() {
			var s stockRow
			if err := gapRows.Scan(&s.ID, &s.Ticker); err == nil {
				allStocks = append(allStocks, s)
			}
		}
		gapRows.Close()

		for i, s := range allStocks {
			missing, err := getMissingDates(ctx, db, s.Ticker, time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC), lastDate)
			if err != nil {
				slog.Error("Gap check failed", "ticker", s.Ticker, "error", err)
				continue
			}

			// Only backfill gaps from the last 14 days — older "gaps" are just weekends/holidays
			cutoff := time.Now().AddDate(0, 0, -14)
			var recentMissing []time.Time
			for _, m := range missing {
				if m.After(cutoff) {
					recentMissing = append(recentMissing, m)
				}
			}

			if len(recentMissing) > 0 {
				from := recentMissing[0].Format("2006-01-02")
				to := recentMissing[len(recentMissing)-1].Format("2006-01-02")
				slog.Warn("Found recent gaps, backfilling", "ticker", s.Ticker, "missing_days", len(recentMissing), "from", from, "to", to)

				fetchAndSaveStock(ctx, db, apiKey, s.ID, s.Ticker, from, to)
				slog.Info("✅ Gap backfilled", "ticker", s.Ticker)

				if i < len(allStocks)-1 {
					time.Sleep(12 * time.Second)
				}
			}
		}
	}
	startDate := lastDate.AddDate(0, 0, 1)

	slog.Info("Last saved date", "date", lastDate.Format("2006-01-02"))
	slog.Info("Fetching data from", "date", startDate.Format("2006-01-02"))

	updateAllStocksPrices(ctx, db, apiKey, startDate)
// 	recalculateIndex(ctx, db)

	slog.Info("✅ Daily updater finished successfully!")
}

func getLastSavedDate(ctx context.Context, db *pgxpool.Pool) (time.Time, error) {
	var lastDate time.Time
	err := db.QueryRow(ctx, "SELECT MAX(price_date) FROM prices").Scan(&lastDate)
	if err != nil || lastDate.IsZero() {
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil
	}
	return lastDate.UTC(), nil
}

func getMissingDates(ctx context.Context, db *pgxpool.Pool, ticker string, from, to time.Time) ([]time.Time, error) {
	rows, err := db.Query(ctx, `
		SELECT generate_series($1::date, $2::date, '1 day')::date AS expected_date
		EXCEPT
		SELECT p.price_date
		FROM prices p
		JOIN stocks s ON s.id = p.stock_id
		WHERE s.ticker = $3
		ORDER BY 1
	`, from, to, ticker)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var missing []time.Time
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		// skip weekends
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			missing = append(missing, d)
		}
	}
	return missing, nil
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


func recalculateIndex(ctx context.Context, db *pgxpool.Pool) error {
	// 1. Universe: tech + finance, active
	rows, err := db.Query(ctx, `
		SELECT id FROM stocks WHERE active = TRUE AND sector IN ('tech', 'finance')
	`)
	if err != nil {
		return err
	}
	var stockIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		stockIDs = append(stockIDs, id)
	}
	rows.Close()
	numStocks := len(stockIDs)
	if numStocks == 0 {
		return fmt.Errorf("no active tech/finance stocks found")
	}

	// 2. Pull all prices for the universe from base date forward
	priceRows, err := db.Query(ctx, `
		SELECT stock_id, price_date, close_price
		FROM prices
		WHERE stock_id = ANY($1) AND price_date >= $2
		ORDER BY price_date ASC
	`, stockIDs, BASE_DATE)
	if err != nil {
		return err
	}
	defer priceRows.Close()

	prices := make(map[int]map[string]float64)
	var allDates []string
	dateSeen := make(map[string]bool)

	for priceRows.Next() {
		var stockID int
		var priceDate time.Time
		var close float64
		if err := priceRows.Scan(&stockID, &priceDate, &close); err != nil {
			return err
		}
		dateStr := priceDate.Format("2006-01-02")
		if prices[stockID] == nil {
			prices[stockID] = make(map[string]float64)
		}
		prices[stockID][dateStr] = close
		if !dateSeen[dateStr] {
			dateSeen[dateStr] = true
			allDates = append(allDates, dateStr)
		}
	}
	sort.Strings(allDates)

	// 3. Walk through dates, tracking quarter base prices and compounding anchor
	quarterBase := make(map[int]float64)
	currentQuarterIdx := -1
	carryAnchor := 1.0
	lastRelOfPrevQuarter := 1.0

	type result struct {
		date       string
		indexValue float64
	}
	var results []result

	for _, d := range allDates {
		quarterIdx := -1
		for i, rd := range QUARTERLY_REBALANCE_DATES {
			if d >= rd {
				quarterIdx = i
			}
		}
		if quarterIdx == -1 {
			continue
		}

		if quarterIdx != currentQuarterIdx {
			if currentQuarterIdx != -1 {
				carryAnchor *= lastRelOfPrevQuarter
			}
			currentQuarterIdx = quarterIdx
			quarterBase = make(map[int]float64)
			for _, sid := range stockIDs {
				if p, ok := prices[sid][d]; ok {
					quarterBase[sid] = p
				}
			}
		}

		var sumRel float64
		var count int
		for _, sid := range stockIDs {
			base, hasBase := quarterBase[sid]
			today, hasToday := prices[sid][d]
			if hasBase && hasToday && base != 0 {
				sumRel += today / base
				count++
			}
		}
		if count != numStocks {
			continue
		}
		avgRel := sumRel / float64(count)
		lastRelOfPrevQuarter = avgRel
		indexValue := 100.0 * carryAnchor * avgRel

		results = append(results, result{date: d, indexValue: indexValue})
	}

	// 4. Write results with daily_change
	for i, r := range results {
		var dailyChange float64
		if i > 0 {
			dailyChange = r.indexValue - results[i-1].indexValue
		}
		_, err := db.Exec(ctx, `
			INSERT INTO index_values (price_date, index_value, daily_change)
			VALUES ($1, $2, $3)
			ON CONFLICT (price_date) DO UPDATE
				SET index_value = EXCLUDED.index_value,
				    daily_change = EXCLUDED.daily_change
		`, r.date, r.indexValue, dailyChange)
		if err != nil {
			return err
		}
	}

	slog.Info("✅ Tech+Finance index recalculated", "base_date", BASE_DATE, "days", len(results))
	return nil
}