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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	MANSA_BASE_URL  = "https://mansaapi.com/api/v1/markets/exchanges/NSE/stocks?limit=100"
	DATE_LAYOUT     = "2006-01-02"
	BASE_DATE       = "2026-09-14" // inception — index = 100 here
	BASE_INDEX      = 100.0

	REQUEST_TIMEOUT = 60 * time.Second
)

// Rebalance dates — the ONLY dates on which constituent weights are allowed
// to change. Between these, index_shares are held fixed so the index moves
// purely on price, per whitepaper §4.4/§4.5. First entry MUST equal BASE_DATE
// (inception is itself a rebalance). Kept as an explicit list rather than a
// generated +3-months schedule so odd calendar cases (holidays, etc.) can be
// hand-adjusted before they happen.
var REBALANCE_DATES = []string{
	BASE_DATE,    // inception
	"2026-12-14",
	"2027-03-14",
	"2027-06-14",
	"2027-09-14",
	"2027-12-14",
	"2028-03-14",
}

// Note on universe: the `stocks` table is assumed pre-filtered upstream to
// exactly the screened constituent set (payout <80%, 3yr continuity, etc.) —
// this file does not re-apply that screen. It only excludes rows with
// dividend_yield_ttm <= 0 as a data-sanity guard against divide-by-zero, not
// as a selection rule.

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

	slog.Info("🚀 NSE Dividend Composite (IDCI) updater starting...", "base_date", BASE_DATE)

	// Skip weekends — NSE does not trade on weekends.
	// NOTE: this does NOT account for Kenyan public holidays. On a holiday,
	// Mansa may return the prior close as if it were fresh, which will look
	// like a flat/stale price in the series. Add a holiday calendar check
	// here before this runs unattended for long stretches.
	if wd := time.Now().UTC().Weekday(); wd == time.Saturday || wd == time.Sunday {
		slog.Info("📅 Weekend — NSE closed, skipping update", "day", wd)
		return
	}

	today := time.Now().UTC().Format(DATE_LAYOUT)

	if today < BASE_DATE {
		slog.Info("⏳ Before base date — collecting prices only, no index calc yet",
			"today", today, "base_date", BASE_DATE)
	}

	// ── Step 1: Fetch & save today's prices from Mansa ───────────────────────
	slog.Info("📥 Fetching NSE prices from Mansa API...")
	stocks, err := fetchMansaPrices(apiKey)
	if err != nil {
		slog.Error("Mansa API fetch failed", "error", err)
		os.Exit(1)
	}
	slog.Info("✅ Fetched prices", "count", len(stocks))

	saved, err := savePrices(ctx, db, stocks, today)
	if err != nil {
		slog.Error("Price save failed", "error", err)
		os.Exit(1)
	}
	slog.Info("💾 Prices saved", "rows", saved)

	if today < BASE_DATE {
		return // nothing to index yet
	}

	// ── Step 2: Backfill any missing index dates, in order ───────────────────
	// Order matters: rebalances must be applied sequentially so each date's
	// active index_shares reflect every rebalance that preceded it.
	missing, err := getMissingIndexDates(ctx, db)
	if err != nil {
		slog.Error("Missing dates check failed", "error", err)
		os.Exit(1)
	}

	if len(missing) == 0 {
		slog.Info("✅ Index already up to date")
		return
	}

	slog.Info("📅 Processing index dates", "count", len(missing), "dates", missing)
	for _, date := range missing {
		if err := calculateAndSaveIndex(ctx, db, date); err != nil {
			slog.Error("Index calc failed", "date", date, "error", err)
			// stop rather than continue — a later date's correctness depends
			// on this one if it happens to be a rebalance date
			os.Exit(1)
		}
	}

	slog.Info("✅ Daily update complete", "date", today)
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

// ── isRebalanceDate ────────────────────────────────────────────────────────────

func isRebalanceDate(date string) bool {
	for _, d := range REBALANCE_DATES {
		if d == date {
			return true
		}
	}
	return false
}

// previousRebalanceDate returns the latest rebalance date strictly BEFORE the
// given date, or "" if none (i.e. date is at or before inception).
func previousRebalanceDate(date string) string {
	prev := ""
	for _, d := range REBALANCE_DATES {
		if d < date {
			prev = d
		} else {
			break
		}
	}
	return prev
}

// activeRebalanceDate returns the latest rebalance date AT OR BEFORE the
// given date — i.e. which weighting period `date` falls into.
func activeRebalanceDate(date string) string {
	active := ""
	for _, d := range REBALANCE_DATES {
		if d <= date {
			active = d
		} else {
			break
		}
	}
	return active
}

// ── getYieldWeightsAsOf ────────────────────────────────────────────────────────

// getYieldWeightsAsOf returns yield weights using only fundamentals known
// as of asOfDate (as_of_date <= asOfDate, most recent per stock). This is
// what makes backfilled/rebalanced dates point-in-time correct instead of
// leaking future data into past index values.
func getYieldWeightsAsOf(ctx context.Context, db *pgxpool.Pool, asOfDate string) (map[string]float64, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT ON (s.ticker) s.ticker, f.dividend_yield_ttm
		FROM fundamentals f
		JOIN stocks s ON s.id = f.stock_id
		WHERE s.active = TRUE
		  AND f.as_of_date <= $1
		  AND f.dividend_yield_ttm > 0
		ORDER BY s.ticker, f.as_of_date DESC
	`, asOfDate)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if totalYield == 0 {
		return nil, fmt.Errorf("total yield is zero as of %s", asOfDate)
	}

	weights := make(map[string]float64, len(yields))
	for ticker, yield := range yields {
		weights[ticker] = yield / totalYield
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
	return prices, rows.Err()
}

// ── stockID ────────────────────────────────────────────────────────────────────

func stockID(ctx context.Context, db *pgxpool.Pool, ticker string) (int, error) {
	var id int
	err := db.QueryRow(ctx, `SELECT id FROM stocks WHERE ticker = $1 AND active = TRUE`, ticker).Scan(&id)
	return id, err
}

// ── setIndexShares ────────────────────────────────────────────────────────────

// setIndexShares computes and stores index_shares for a rebalance date, given
// the index value in effect at the moment of rebalance (indexValueAtRebalance)
// and that date's closing prices. This is the "index share" mechanism from
// whitepaper §4.4:
//
//	S_i = ( w_i × IndexValue ) / P_i
//
// which fixes weights until the next rebalance while keeping the index level
// continuous across the reset (no jump on the rebalance date itself).
func setIndexShares(ctx context.Context, db *pgxpool.Pool, rebalanceDate string, indexValueAtRebalance float64) error {
	weights, err := getYieldWeightsAsOf(ctx, db, rebalanceDate)
	if err != nil {
		return fmt.Errorf("yield weights as of %s: %w", rebalanceDate, err)
	}
	prices, err := getPricesOnDate(ctx, db, rebalanceDate)
	if err != nil {
		return fmt.Errorf("prices on %s: %w", rebalanceDate, err)
	}

	type row struct {
		ticker string
		weight float64
	}
	var sorted []row
	for t, w := range weights {
		sorted = append(sorted, row{t, w})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].weight > sorted[j].weight })

	var missing []string
	var totalWeightUsed float64
	type shareRow struct {
		ticker  string
		weight  float64
		shares  float64
	}
	var toInsert []shareRow

	for _, r := range sorted {
		price, ok := prices[r.ticker]
		if !ok || price <= 0 {
			missing = append(missing, r.ticker)
			continue
		}
		shares := (r.weight * indexValueAtRebalance) / price
		toInsert = append(toInsert, shareRow{r.ticker, r.weight, shares})
		totalWeightUsed += r.weight
	}

	if len(missing) > 0 {
		slog.Warn("⚠️  Constituents missing a price on rebalance date — excluded from this period's weights",
			"rebalance_date", rebalanceDate, "tickers", missing)
	}
	if totalWeightUsed == 0 {
		return fmt.Errorf("no constituents priced on rebalance date %s", rebalanceDate)
	}

	// Renormalize remaining weights to sum to 1 if any constituent was
	// dropped for lack of a price, then recompute shares against that.
	for i := range toInsert {
		toInsert[i].weight = toInsert[i].weight / totalWeightUsed
		toInsert[i].shares = (toInsert[i].weight * indexValueAtRebalance) / prices[toInsert[i].ticker]
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, s := range toInsert {
		id, err := stockID(ctx, db, s.ticker)
		if err != nil {
			return fmt.Errorf("stock id for %s: %w", s.ticker, err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO index_shares (rebalance_date, stock_id, weight_pct, index_shares)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (rebalance_date, stock_id) DO UPDATE
				SET weight_pct = EXCLUDED.weight_pct,
				    index_shares = EXCLUDED.index_shares
		`, rebalanceDate, id, s.weight*100, s.shares)
		if err != nil {
			return fmt.Errorf("insert index_shares for %s: %w", s.ticker, err)
		}
		slog.Info("⚖️  Weight set", "rebalance_date", rebalanceDate, "ticker", s.ticker,
			"weight_pct", fmt.Sprintf("%.3f%%", s.weight*100))
	}

	return tx.Commit(ctx)
}

// ── getActiveShares ───────────────────────────────────────────────────────────

// getActiveShares returns the index_shares in effect for asOfDate — i.e.
// those stored under the latest rebalance_date <= asOfDate.
func getActiveShares(ctx context.Context, db *pgxpool.Pool, asOfDate string) (map[string]float64, string, error) {
	period := activeRebalanceDate(asOfDate)
	if period == "" {
		return nil, "", fmt.Errorf("no rebalance period covers %s (before inception %s)", asOfDate, BASE_DATE)
	}

	rows, err := db.Query(ctx, `
		SELECT s.ticker, idx.index_shares
		FROM index_shares idx
		JOIN stocks s ON s.id = idx.stock_id
		WHERE idx.rebalance_date = $1
	`, period)
	if err != nil {
		return nil, period, err
	}
	defer rows.Close()

	shares := make(map[string]float64)
	for rows.Next() {
		var ticker string
		var sh float64
		if err := rows.Scan(&ticker, &sh); err != nil {
			continue
		}
		shares[ticker] = sh
	}
	if err := rows.Err(); err != nil {
		return nil, period, err
	}
	if len(shares) == 0 {
		return nil, period, fmt.Errorf("index_shares not yet computed for rebalance period %s", period)
	}
	return shares, period, nil
}

// ── computeIndexValue ─────────────────────────────────────────────────────────

// computeIndexValue = Σ( index_shares_i × price_i(date) ), using whichever
// rebalance period is active on `date`. No re-derivation of weights here —
// that only happens in setIndexShares, on rebalance dates.
func computeIndexValue(ctx context.Context, db *pgxpool.Pool, date string) (float64, error) {
	shares, period, err := getActiveShares(ctx, db, date)
	if err != nil {
		return 0, err
	}
	prices, err := getPricesOnDate(ctx, db, date)
	if err != nil {
		return 0, err
	}
	if len(prices) == 0 {
		return 0, fmt.Errorf("no prices for %s", date)
	}

	var value float64
	var missing []string
	for ticker, sh := range shares {
		price, ok := prices[ticker]
		if !ok || price <= 0 {
			missing = append(missing, ticker)
			continue
		}
		value += sh * price
	}
	if len(missing) > 0 {
		slog.Warn("⚠️  Missing today's price for some constituents — index computed on remainder",
			"date", date, "period", period, "tickers", missing)
	}
	if value == 0 {
		return 0, fmt.Errorf("index value is zero for %s — no priced constituents", date)
	}
	return value, nil
}

// ── calculateAndSaveIndex ─────────────────────────────────────────────────────

func calculateAndSaveIndex(ctx context.Context, db *pgxpool.Pool, date string) error {
	// Inception: force index = 100 and derive the FIRST set of shares
	// directly from it, per §4.4 (S_i,0 = w_i,0 × 100 / P_i,0).
	if date == BASE_DATE {
		if err := setIndexShares(ctx, db, BASE_DATE, BASE_INDEX); err != nil {
			return fmt.Errorf("inception shares: %w", err)
		}
		return saveIndexValue(ctx, db, date, BASE_INDEX)
	}

	// Regular day: compute using whichever shares are currently active.
	value, err := computeIndexValue(ctx, db, date)
	if err != nil {
		return err
	}
	if err := saveIndexValue(ctx, db, date, value); err != nil {
		return err
	}

	// If `date` is itself a rebalance date, the value just computed and
	// saved (using the OLD shares) becomes the anchor for the NEW shares —
	// this is what keeps the series continuous across the reset: same day,
	// same price, same index level, only the going-forward weights change.
	if isRebalanceDate(date) {
		slog.Info("🔄 Rebalance date reached — resetting index shares", "date", date)
		if err := setIndexShares(ctx, db, date, value); err != nil {
			return fmt.Errorf("rebalance shares on %s: %w", date, err)
		}
	}

	return nil
}

// ── saveIndexValue ────────────────────────────────────────────────────────────

func saveIndexValue(ctx context.Context, db *pgxpool.Pool, date string, value float64) error {
	var prevValue float64
	var hasPrev bool
	err := db.QueryRow(ctx, `
		SELECT index_value FROM index_values
		WHERE price_date < $1
		ORDER BY price_date DESC
		LIMIT 1
	`, date).Scan(&prevValue)
	switch {
	case err == nil:
		hasPrev = true
	case err == pgx.ErrNoRows:
		hasPrev = false
	default:
		return fmt.Errorf("lookup previous index value: %w", err) // real DB error — don't silently default
	}

	dailyChange := 0.0
	if hasPrev {
		dailyChange = value - prevValue
	}

	_, err = db.Exec(ctx, `
		INSERT INTO index_values (price_date, index_value, daily_change)
		VALUES ($1, $2, $3)
		ON CONFLICT (price_date) DO UPDATE
			SET index_value = EXCLUDED.index_value,
			    daily_change = EXCLUDED.daily_change
	`, date, value, dailyChange)
	if err != nil {
		return fmt.Errorf("index save: %w", err)
	}

	slog.Info("📊 Index saved", "date", date, "value", fmt.Sprintf("%.4f", value),
		"change", fmt.Sprintf("%+.4f", dailyChange))
	return nil
}

// ── getMissingIndexDates ──────────────────────────────────────────────────────

func getMissingIndexDates(ctx context.Context, db *pgxpool.Pool) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT p.price_date
		FROM prices p
		JOIN stocks s ON s.id = p.stock_id
		WHERE s.active = TRUE
		  AND p.price_date >= $1
		  AND p.price_date NOT IN (SELECT price_date FROM index_values)
		ORDER BY p.price_date ASC
	`, BASE_DATE)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dates []string
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			continue
		}
		dates = append(dates, d.Format(DATE_LAYOUT))
	}
	return dates, rows.Err()
}