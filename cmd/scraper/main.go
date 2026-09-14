package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"golang.org/x/net/html"
)

const (
	SCRAPE_URL      = "https://www.mansamarkets.com/kenya"
	BASE_DATE       = "2026-08-31"
	BASE_INDEX      = 100.0
	REQUEST_TIMEOUT = 30 * time.Second
)

var REBALANCE_DATES = []string{
	"2026-08-31",
	"2027-03-01",
	"2027-09-01",
	"2028-03-01",
}

type StockPrice struct {
	Ticker string
	Price  float64
	Volume int64
}

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

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("Missing DATABASE_URL")
		os.Exit(1)
	}

	// Skip weekends
	if wd := time.Now().UTC().Weekday(); wd == time.Saturday || wd == time.Sunday {
		slog.Info("📅 Weekend — NSE closed, skipping", "day", wd)
		return
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("DB connection failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	slog.Info("🚀 NSE Dividend Index updater starting...")
	// Use last weekday as the price date
	// mansamarkets shows end-of-day prices for the last closed session
	now := time.Now().UTC()
	priceDate := now
	if now.Weekday() == time.Monday {
		// Monday morning — prices are from Friday
		priceDate = now.AddDate(0, 0, -3)
	} else if now.Hour() < 15 {
		// Before 3 PM EAT (12:00 UTC) — prices are from yesterday
		priceDate = now.AddDate(0, 0, -1)
	}
	today := priceDate.Format("2006-01-02")

	// Step 1: Insert base row if missing
	if err := insertBaseIfMissing(ctx, db); err != nil {
		slog.Error("Base insert failed", "error", err)
		os.Exit(1)
	}

	// Step 2: Scrape prices from mansamarkets.com
	slog.Info("🌍 Scraping NSE prices from mansamarkets.com...")
	var stocks []StockPrice
	for attempt := 1; attempt <= 3; attempt++ {
		stocks, err = scrapeMansamarkets()
		if err == nil && len(stocks) > 0 {
			slog.Info("✅ Scraped prices", "count", len(stocks))
			break
		}
		slog.Warn("Scrape attempt failed", "attempt", attempt, "error", err)
		if attempt < 3 {
			time.Sleep(time.Duration(attempt*15) * time.Second)
		}
	}

	if len(stocks) == 0 {
		slog.Warn("⚠️ No prices scraped — running index calc on existing data only")
	} else {
		saved, err := savePrices(ctx, db, stocks, today)
		if err != nil {
			slog.Error("Price save failed", "error", err)
		} else {
			slog.Info("💾 Prices saved", "rows", saved)
		}
	}

	// Step 3: Backfill any missing index dates
	missing, err := getMissingIndexDates(ctx, db)
	if err != nil {
		slog.Error("Missing dates check failed", "error", err)
		os.Exit(1)
	}

	if len(missing) == 0 {
		slog.Info("✅ Index already up to date")
	} else {
		slog.Info("📅 Backfilling missing dates", "count", len(missing), "dates", missing)
		for _, date := range missing {
			if err := calculateAndSaveIndex(ctx, db, date); err != nil {
				slog.Error("Index calc failed", "date", date, "error", err)
			}
		}
	}

	slog.Info("✅ Daily update complete", "date", today)
}

// ── scrapeMansamarkets ────────────────────────────────────────────────────────

func scrapeMansamarkets() ([]StockPrice, error) {
	client := &http.Client{Timeout: REQUEST_TIMEOUT}
	req, err := http.NewRequest("GET", SCRAPE_URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NSE-Index-Bot/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:200]))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseStockTable(string(body))
}

// ── parseStockTable ───────────────────────────────────────────────────────────

func parseStockTable(htmlContent string) ([]StockPrice, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return nil, err
	}

	var stocks []StockPrice
	var parseNode func(*html.Node)

	inTable := false
	var currentRow []string

	parseNode = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "table":
				inTable = true
			case "tr":
				if inTable {
					currentRow = []string{}
				}
			case "td":
				if inTable {
					text := extractText(n)
					currentRow = append(currentRow, strings.TrimSpace(text))
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			parseNode(c)
		}

		// After processing tr — try to extract stock data
		if n.Type == html.ElementNode && n.Data == "tr" && inTable && len(currentRow) >= 3 {
			ticker, price, volume := extractStockFromRow(currentRow)
			if ticker != "" && price > 0 {
				stocks = append(stocks, StockPrice{
					Ticker: ticker,
					Price:  price,
					Volume: volume,
				})
			}
		}

		if n.Type == html.ElementNode && n.Data == "table" {
			inTable = false
		}
	}

	parseNode(doc)
	return stocks, nil
}

func extractText(n *html.Node) string {
	var text strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return text.String()
}

func extractStockFromRow(row []string) (ticker string, price float64, volume int64) {
	// Row format from mansamarkets table:
	// [#, Company Name TICKER, KShXXX.XX, change, chg%, volume, shares, mktcap]
	if len(row) < 3 {
		return "", 0, 0
	}

	// Extract ticker — usually in format "Company Name\nTICKER" or just "TICKER"
	for _, cell := range row[:3] {
		// Look for NSE ticker pattern (2-4 uppercase letters)
		parts := strings.Fields(cell)
		for _, part := range parts {
			if len(part) >= 2 && len(part) <= 5 && part == strings.ToUpper(part) &&
				!strings.Contains(part, "KSh") && !strings.Contains(part, "%") {
				ticker = part
			}
		}
	}

	// Extract price — look for KShXXX.XX pattern
	for _, cell := range row {
		cleaned := strings.ReplaceAll(cell, "KSh", "")
		cleaned = strings.ReplaceAll(cleaned, ",", "")
		cleaned = strings.TrimSpace(cleaned)
		if p, err := strconv.ParseFloat(cleaned, 64); err == nil && p > 0 && p < 100000 {
			price = p
			break
		}
	}

	// Extract volume if available
	for _, cell := range row {
		cleaned := strings.ReplaceAll(cell, ",", "")
		cleaned = strings.ReplaceAll(cleaned, "M", "000000")
		cleaned = strings.ReplaceAll(cleaned, "K", "000")
		cleaned = strings.TrimSpace(cleaned)
		if v, err := strconv.ParseInt(cleaned, 10, 64); err == nil && v > 0 {
			volume = v
			break
		}
	}

	return ticker, price, volume
}

// ── insertBaseIfMissing ───────────────────────────────────────────────────────

func insertBaseIfMissing(ctx context.Context, db *pgxpool.Pool) error {
	var count int
	db.QueryRow(ctx, "SELECT COUNT(*) FROM index_values WHERE price_date = $1", BASE_DATE).Scan(&count)
	if count > 0 {
		return nil
	}
	_, err := db.Exec(ctx, `
		INSERT INTO index_values (price_date, index_value, daily_change)
		VALUES ($1, $2, 0) ON CONFLICT (price_date) DO NOTHING
	`, BASE_DATE, BASE_INDEX)
	if err == nil {
		slog.Info("📌 Base date row inserted", "date", BASE_DATE, "value", BASE_INDEX)
	}
	return err
}

// ── savePrices ────────────────────────────────────────────────────────────────

func savePrices(ctx context.Context, db *pgxpool.Pool, stocks []StockPrice, date string) (int, error) {
	saved := 0
	for _, s := range stocks {
		if s.Price <= 0 {
			continue
		}
		tag, err := db.Exec(ctx, `
			INSERT INTO prices (stock_id, price_date, close_price, volume)
			SELECT id, $2, $3, $4 FROM stocks
			WHERE ticker = $1 AND active = TRUE
			ON CONFLICT (stock_id, price_date) DO UPDATE
				SET close_price = EXCLUDED.close_price,
				    volume = EXCLUDED.volume
		`, s.Ticker, date, s.Price, s.Volume)
		if err != nil {
			slog.Warn("Price save skipped", "ticker", s.Ticker, "error", err)
			continue
		}
		if tag.RowsAffected() > 0 {
			saved++
			slog.Info("✓ Price saved", "ticker", s.Ticker, "price", s.Price)
		}
	}
	return saved, nil
}

// ── getMissingIndexDates ──────────────────────────────────────────────────────

func getMissingIndexDates(ctx context.Context, db *pgxpool.Pool) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT p.price_date
		FROM prices p
		JOIN stocks s ON s.id = p.stock_id
		WHERE s.active = TRUE
		  AND p.price_date > $1
		  AND p.price_date NOT IN (SELECT price_date FROM index_values)
		  AND EXTRACT(DOW FROM p.price_date) NOT IN (0, 6)
		ORDER BY 1 ASC
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
		dates = append(dates, d.Format("2006-01-02"))
	}
	return dates, nil
}

// ── calculateAndSaveIndex ─────────────────────────────────────────────────────

func calculateAndSaveIndex(ctx context.Context, db *pgxpool.Pool, today string) error {
	weights, err := getYieldWeights(ctx, db)
	if err != nil {
		return fmt.Errorf("yield weights: %w", err)
	}

	basePrices, err := getPricesOnDate(ctx, db, BASE_DATE)
	if err != nil {
		return fmt.Errorf("base prices: %w", err)
	}

	todayPrices, err := getPricesOnDate(ctx, db, today)
	if err != nil {
		return fmt.Errorf("today prices: %w", err)
	}

	if len(todayPrices) == 0 {
		slog.Warn("No prices for date — skipping", "date", today)
		return nil
	}

	var indexValue, totalWeightUsed float64
	var missing []string

	for ticker, weight := range weights {
		basePrice, hasBase := basePrices[ticker]
		todayPrice, hasToday := todayPrices[ticker]

		if !hasBase || basePrice == 0 {
			missing = append(missing, ticker+"(no base)")
			continue
		}
		if !hasToday || todayPrice == 0 {
			missing = append(missing, ticker+"(no price)")
			continue
		}

		indexValue += weight * (todayPrice / basePrice)
		totalWeightUsed += weight
	}

	if totalWeightUsed == 0 {
		return fmt.Errorf("no valid stocks")
	}
	if len(missing) > 0 {
		slog.Warn("Stocks skipped", "tickers", missing)
	}
	if totalWeightUsed < 1.0 {
		indexValue = indexValue / totalWeightUsed
	}
	indexValue *= BASE_INDEX

	var prevValue float64
	db.QueryRow(ctx, `
		SELECT index_value FROM index_values
		WHERE price_date < $1
		ORDER BY price_date DESC LIMIT 1
	`, today).Scan(&prevValue)
	if prevValue == 0 {
		prevValue = BASE_INDEX
	}
	dailyChange := indexValue - prevValue

	_, err = db.Exec(ctx, `
		INSERT INTO index_values (price_date, index_value, daily_change)
		VALUES ($1, $2, $3)
		ON CONFLICT (price_date) DO UPDATE
			SET index_value = EXCLUDED.index_value,
			    daily_change = EXCLUDED.daily_change
	`, today, indexValue, dailyChange)
	if err != nil {
		return err
	}

	slog.Info("📊 Index calculated",
		"date", today,
		"value", fmt.Sprintf("%.4f", indexValue),
		"change", fmt.Sprintf("%+.4f", dailyChange),
		"stocks", len(weights)-len(missing),
	)
	return nil
}

// ── getYieldWeights ───────────────────────────────────────────────────────────

func getYieldWeights(ctx context.Context, db *pgxpool.Pool) (map[string]float64, error) {
	rows, err := db.Query(ctx, `
		SELECT s.ticker, f.dividend_yield_ttm
		FROM fundamentals f
		JOIN stocks s ON s.id = f.stock_id
		WHERE s.active = TRUE
		  AND f.as_of_date = (
			SELECT MAX(as_of_date) FROM fundamentals f2 WHERE f2.stock_id = f.stock_id
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

	weights := make(map[string]float64)
	for ticker, yield := range yields {
		weights[ticker] = yield / totalYield
	}

	type tw struct {
		ticker string
		weight float64
	}
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