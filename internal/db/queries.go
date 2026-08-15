/**package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/koros33/yindex/internal/models"

// periodToDate converts a period string to a start date
func periodToDate(period string) (time.Time, error) {
	now := time.Now().UTC()
	switch period {
	case "1d":
		return now.AddDate(0, 0, -1), nil
	case "1w":
		return now.AddDate(0, 0, -7), nil
	case "1m":
		return now.AddDate(0, -1, 0), nil
	case "3m":
		return now.AddDate(0, -3, 0), nil
	case "6m":
		return now.AddDate(0, -6, 0), nil
	case "ytd":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC), nil
	case "1y":
		return now.AddDate(-1, 0, 0), nil
	case "all":
		return time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC), nil
	default:
		return time.Time{}, fmt.Errorf("invalid period: %s", period)
	}
}

// ── Index Queries ─────────────────────────────────────────────────────────────

func GetIndexLatest(ctx context.Context, db *pgxpool.Pool) (*models.IndexLatest, error) {
	var iv models.IndexLatest
	err := db.QueryRow(ctx, `
		SELECT price_date, index_value, COALESCE(daily_change, 0)
		FROM momentum_index_values
		ORDER BY price_date DESC
		LIMIT 1
	`).Scan(&iv.Date, &iv.IndexValue, &iv.DailyChange)
	if err != nil {
		return nil, err
	}
	prev := iv.IndexValue - iv.DailyChange
	if prev != 0 {
		iv.DailyPct = (iv.DailyChange / prev) * 100
	}
	return &iv, nil
}

func GetIndexHistory(ctx context.Context, db *pgxpool.Pool, period string) ([]models.IndexValue, error) {
	from, err := periodToDate(period)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT price_date, index_value, COALESCE(daily_change, 0)
		FROM momentum_index_values
		WHERE price_date >= $1
		ORDER BY price_date ASC
	`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.IndexValue
	for rows.Next() {
		var iv models.IndexValue
		if err := rows.Scan(&iv.Date, &iv.IndexValue, &iv.DailyChange); err != nil {
			continue
		}
		result = append(result, iv)
	}
	return result, nil
}

func GetIndexStats(ctx context.Context, db *pgxpool.Pool) (*models.IndexStats, error) {
	var stats models.IndexStats
	baseDate := time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC)
	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	oneMonthAgo := time.Now().AddDate(0, -1, 0)

	// ── FIX: actually scan the result ────────────────────────────────────────
	err := db.QueryRow(ctx, `
		WITH latest AS (
			SELECT index_value FROM momentum_index_values ORDER BY price_date DESC LIMIT 1
		),
		base AS (
			SELECT index_value FROM momentum_index_values WHERE price_date = $1 LIMIT 1
		),
		ytd_start AS (
			SELECT index_value FROM momentum_index_values WHERE price_date >= $2 ORDER BY price_date ASC LIMIT 1
		),
		one_month AS (
			SELECT index_value FROM momentum_index_values WHERE price_date >= $3 ORDER BY price_date ASC LIMIT 1
		),
		extremes AS (
			SELECT MAX(index_value) AS high, MIN(index_value) AS low FROM momentum_index_values
		)
		SELECT
			latest.index_value,
			extremes.high,
			extremes.low,
			latest.index_value - ytd_start.index_value,
			CASE WHEN ytd_start.index_value != 0
				THEN ((latest.index_value - ytd_start.index_value) / ytd_start.index_value) * 100
				ELSE 0 END,
			CASE WHEN one_month.index_value != 0
				THEN ((latest.index_value - one_month.index_value) / one_month.index_value) * 100
				ELSE 0 END,
			base.index_value
		FROM latest, extremes, ytd_start, one_month, base
	`, baseDate, ytdStart, oneMonthAgo).Scan(
		&stats.Current,
		&stats.AllTimeHigh,
		&stats.AllTimeLow,
		&stats.YTDChange,
		&stats.YTDPct,
		&stats.OneMonthPct,
		&stats.BaseValue,
	)
	if err != nil {
		return nil, err
	}

	stats.BaseDate = baseDate
	return &stats, nil
}

// ── Stock Queries ─────────────────────────────────────────────────────────────

func GetAllStocks(ctx context.Context, db *pgxpool.Pool) ([]models.Stock, error) {
	rows, err := db.Query(ctx, `
		SELECT id, ticker, name, active FROM stocks WHERE active = TRUE ORDER BY ticker
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stocks []models.Stock
	for rows.Next() {
		var s models.Stock
		if err := rows.Scan(&s.ID, &s.Ticker, &s.Name, &s.Active); err != nil {
			continue
		}
		stocks = append(stocks, s)
	}
	return stocks, nil
}

func GetStockByTicker(ctx context.Context, db *pgxpool.Pool, ticker string) (*models.Stock, error) {
	var s models.Stock
	err := db.QueryRow(ctx, `
		SELECT id, ticker, name, active FROM stocks WHERE ticker = $1
	`, ticker).Scan(&s.ID, &s.Ticker, &s.Name, &s.Active)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func GetStockHistory(ctx context.Context, db *pgxpool.Pool, ticker, period string) ([]models.StockPrice, error) {
	from, err := periodToDate(period)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT p.price_date, p.close_price
		FROM prices p
		JOIN stocks s ON s.id = p.stock_id
		WHERE s.ticker = $1 AND p.price_date >= $2
		ORDER BY p.price_date ASC
	`, ticker, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.StockPrice
	for rows.Next() {
		var sp models.StockPrice
		if err := rows.Scan(&sp.Date, &sp.ClosePrice); err != nil {
			continue
		}
		result = append(result, sp)
	}
	return result, nil
}

func GetStockStats(ctx context.Context, db *pgxpool.Pool, ticker string) (*models.StockStats, error) {
	var stats models.StockStats
	oneMonthAgo := time.Now().AddDate(0, -1, 0)
	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)

	// ── FIX: actually scan the result ────────────────────────────────────────
	err := db.QueryRow(ctx, `
		WITH stock AS (
			SELECT id, name FROM stocks WHERE ticker = $1
		),
		latest AS (
			SELECT p.close_price, p.price_date
			FROM prices p JOIN stock s ON s.id = p.stock_id
			ORDER BY p.price_date DESC LIMIT 1
		),
		prev_day AS (
			SELECT p.close_price
			FROM prices p JOIN stock s ON s.id = p.stock_id
			ORDER BY p.price_date DESC OFFSET 1 LIMIT 1
		),
		one_month AS (
			SELECT p.close_price
			FROM prices p JOIN stock s ON s.id = p.stock_id
			WHERE p.price_date >= $2 ORDER BY p.price_date ASC LIMIT 1
		),
		ytd AS (
			SELECT p.close_price
			FROM prices p JOIN stock s ON s.id = p.stock_id
			WHERE p.price_date >= $3 ORDER BY p.price_date ASC LIMIT 1
		),
		extremes AS (
			SELECT MAX(p.close_price) AS high, MIN(p.close_price) AS low
			FROM prices p JOIN stock s ON s.id = p.stock_id
		)
		SELECT
			stock.name,
			latest.close_price,
			latest.price_date,
			CASE WHEN prev_day.close_price != 0
				THEN ((latest.close_price - prev_day.close_price) / prev_day.close_price) * 100
				ELSE 0 END,
			CASE WHEN one_month.close_price != 0
				THEN ((latest.close_price - one_month.close_price) / one_month.close_price) * 100
				ELSE 0 END,
			CASE WHEN ytd.close_price != 0
				THEN ((latest.close_price - ytd.close_price) / ytd.close_price) * 100
				ELSE 0 END,
			extremes.high,
			extremes.low
		FROM stock, latest, prev_day, one_month, ytd, extremes
	`, ticker, oneMonthAgo, ytdStart).Scan(
		&stats.Name,
		&stats.Latest,
		&stats.LastUpdated,
		&stats.DailyPct,
		&stats.OneMonthPct,
		&stats.YTDPct,
		&stats.AllTimeHigh,
		&stats.AllTimeLow,
	)
	if err != nil {
		return nil, err
	}

	stats.Ticker = ticker
	return &stats, nil
}**/

package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/koros33/yindex/internal/models"
)

// periodToDate converts a period string to a start date
func periodToDate(period string) (time.Time, error) {
	now := time.Now().UTC()
	switch period {
	case "1d":
		return now.AddDate(0, 0, -1), nil
	case "1w":
		return now.AddDate(0, 0, -7), nil
	case "1m":
		return now.AddDate(0, -1, 0), nil
	case "3m":
		return now.AddDate(0, -3, 0), nil
	case "6m":
		return now.AddDate(0, -6, 0), nil
	case "ytd":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC), nil
	case "1y":
		return now.AddDate(-1, 0, 0), nil
	case "all":
		return time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC), nil
	default:
		return time.Time{}, fmt.Errorf("invalid period: %s", period)
	}
}

// ── Index Queries ─────────────────────────────────────────────────────────────

func GetIndexLatest(ctx context.Context, db *pgxpool.Pool) (*models.IndexLatest, error) {
	var iv models.IndexLatest
	err := db.QueryRow(ctx, `
		SELECT price_date, index_value, COALESCE(daily_change, 0)
		FROM momentum_index_values
		ORDER BY price_date DESC
		LIMIT 1
	`).Scan(&iv.Date, &iv.IndexValue, &iv.DailyChange)
	if err != nil {
		return nil, err
	}
	prev := iv.IndexValue - iv.DailyChange
	if prev != 0 {
		iv.DailyPct = (iv.DailyChange / prev) * 100
	}
	return &iv, nil
}

func GetIndexHistory(ctx context.Context, db *pgxpool.Pool, period string) ([]models.IndexValue, error) {
	from, err := periodToDate(period)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT price_date, index_value, COALESCE(daily_change, 0)
		FROM momentum_index_values
		WHERE price_date >= $1
		ORDER BY price_date ASC
	`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.IndexValue
	for rows.Next() {
		var iv models.IndexValue
		if err := rows.Scan(&iv.Date, &iv.IndexValue, &iv.DailyChange); err != nil {
			continue
		}
		result = append(result, iv)
	}
	return result, nil
}

func GetIndexStats(ctx context.Context, db *pgxpool.Pool) (*models.IndexStats, error) {
	var stats models.IndexStats
	baseDate := time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC)
	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	oneMonthAgo := time.Now().AddDate(0, -1, 0)

	// ── FIX: actually scan the result ────────────────────────────────────────
	err := db.QueryRow(ctx, `
		WITH latest AS (
			SELECT index_value FROM momentum_index_values ORDER BY price_date DESC LIMIT 1
		),
		base AS (
			SELECT index_value FROM momentum_index_values WHERE price_date = $1 LIMIT 1
		),
		ytd_start AS (
			SELECT index_value FROM momentum_index_values WHERE price_date >= $2 ORDER BY price_date ASC LIMIT 1
		),
		one_month AS (
			SELECT index_value FROM momentum_index_values WHERE price_date >= $3 ORDER BY price_date ASC LIMIT 1
		),
		extremes AS (
			SELECT MAX(index_value) AS high, MIN(index_value) AS low FROM momentum_index_values
		)
		SELECT
			latest.index_value,
			extremes.high,
			extremes.low,
			latest.index_value - ytd_start.index_value,
			CASE WHEN ytd_start.index_value != 0
				THEN ((latest.index_value - ytd_start.index_value) / ytd_start.index_value) * 100
				ELSE 0 END,
			CASE WHEN one_month.index_value != 0
				THEN ((latest.index_value - one_month.index_value) / one_month.index_value) * 100
				ELSE 0 END,
			base.index_value
		FROM latest, extremes, ytd_start, one_month, base
	`, baseDate, ytdStart, oneMonthAgo).Scan(
		&stats.Current,
		&stats.AllTimeHigh,
		&stats.AllTimeLow,
		&stats.YTDChange,
		&stats.YTDPct,
		&stats.OneMonthPct,
		&stats.BaseValue,
	)
	if err != nil {
		return nil, err
	}

	stats.BaseDate = baseDate
	return &stats, nil
}

// ── Stock Queries ─────────────────────────────────────────────────────────────

func GetAllStocks(ctx context.Context, db *pgxpool.Pool) ([]models.Stock, error) {
	rows, err := db.Query(ctx, `
		SELECT id, ticker, name, active FROM stocks WHERE active = TRUE ORDER BY ticker
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stocks []models.Stock
	for rows.Next() {
		var s models.Stock
		if err := rows.Scan(&s.ID, &s.Ticker, &s.Name, &s.Active); err != nil {
			continue
		}
		stocks = append(stocks, s)
	}
	return stocks, nil
}

func GetStockByTicker(ctx context.Context, db *pgxpool.Pool, ticker string) (*models.Stock, error) {
	var s models.Stock
	err := db.QueryRow(ctx, `
		SELECT id, ticker, name, active FROM stocks WHERE ticker = $1
	`, ticker).Scan(&s.ID, &s.Ticker, &s.Name, &s.Active)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func GetStockHistory(ctx context.Context, db *pgxpool.Pool, ticker, period string) ([]models.StockPrice, error) {
	from, err := periodToDate(period)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT p.price_date, p.close_price
		FROM prices p
		JOIN stocks s ON s.id = p.stock_id
		WHERE s.ticker = $1 AND p.price_date >= $2
		ORDER BY p.price_date ASC
	`, ticker, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.StockPrice
	for rows.Next() {
		var sp models.StockPrice
		if err := rows.Scan(&sp.Date, &sp.ClosePrice); err != nil {
			continue
		}
		result = append(result, sp)
	}
	return result, nil
}

func GetStockStats(ctx context.Context, db *pgxpool.Pool, ticker string) (*models.StockStats, error) {
	var stats models.StockStats
	oneMonthAgo := time.Now().AddDate(0, -1, 0)
	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)

	// ── FIX: actually scan the result ────────────────────────────────────────
	err := db.QueryRow(ctx, `
		WITH stock AS (
			SELECT id, name FROM stocks WHERE ticker = $1
		),
		latest AS (
			SELECT p.close_price, p.price_date
			FROM prices p JOIN stock s ON s.id = p.stock_id
			ORDER BY p.price_date DESC LIMIT 1
		),
		prev_day AS (
			SELECT p.close_price
			FROM prices p JOIN stock s ON s.id = p.stock_id
			ORDER BY p.price_date DESC OFFSET 1 LIMIT 1
		),
		one_month AS (
			SELECT p.close_price
			FROM prices p JOIN stock s ON s.id = p.stock_id
			WHERE p.price_date >= $2 ORDER BY p.price_date ASC LIMIT 1
		),
		ytd AS (
			SELECT p.close_price
			FROM prices p JOIN stock s ON s.id = p.stock_id
			WHERE p.price_date >= $3 ORDER BY p.price_date ASC LIMIT 1
		),
		extremes AS (
			SELECT MAX(p.close_price) AS high, MIN(p.close_price) AS low
			FROM prices p JOIN stock s ON s.id = p.stock_id
		)
		SELECT
			stock.name,
			latest.close_price,
			latest.price_date,
			CASE WHEN prev_day.close_price != 0
				THEN ((latest.close_price - prev_day.close_price) / prev_day.close_price) * 100
				ELSE 0 END,
			CASE WHEN one_month.close_price != 0
				THEN ((latest.close_price - one_month.close_price) / one_month.close_price) * 100
				ELSE 0 END,
			CASE WHEN ytd.close_price != 0
				THEN ((latest.close_price - ytd.close_price) / ytd.close_price) * 100
				ELSE 0 END,
			extremes.high,
			extremes.low
		FROM stock, latest, prev_day, one_month, ytd, extremes
	`, ticker, oneMonthAgo, ytdStart).Scan(
		&stats.Name,
		&stats.Latest,
		&stats.LastUpdated,
		&stats.DailyPct,
		&stats.OneMonthPct,
		&stats.YTDPct,
		&stats.AllTimeHigh,
		&stats.AllTimeLow,
	)
	if err != nil {
		return nil, err
	}

	stats.Ticker = ticker
	return &stats, nil
}