# NSE-Pulse

A momentum index engine and stock market API built in Go and Python. Started as a US equity momentum tracker, now pivoting to African markets — specifically the Nairobi Securities Exchange (NSE Kenya).

---

## What This Is

NSE-Pulse calculates a **momentum-weighted stock index** using a methodology inspired by MSCI's momentum factor. It fetches daily price data, computes risk-adjusted momentum scores across a universe of stocks, selects the top performers, and tracks an index value over time. The result is a live, queryable index with a REST API serving price history, stats, and stock data.

The project is split cleanly into three layers:

- **Go** — daily price ingestion from market data APIs, gap detection and backfill, serves the REST API
- **Python** — momentum score calculation, portfolio selection, index value computation
- **Neon (PostgreSQL)** — stores prices, stocks, and index values

---

## The Momentum Index

The index uses a simplified MSCI momentum methodology:

**Score calculation (per stock, per day):**
1. **12-month momentum** — `price[t-1] / price[t-365] - 1`
2. **6-month momentum** — `price[t-1] / price[t-180] - 1`
3. **Combined** — `0.5 × momentum_12m + 0.5 × momentum_6m`
4. **Risk adjustment** — divide by 2-year annualized volatility of daily returns
5. **Winsorize** at 5th/95th percentile to remove outliers
6. **Z-score** cross-sectionally across the universe
7. **Non-linear transform** — `score = 1 + z` if `z > 0`, else `1 / (1 - z)`

**Portfolio construction:**
- Universe: active stocks in the `stocks` table
- Selection: top 30 by momentum score at each reconstitution date
- Weights: equal weight within the selected 30

**Rebalancing schedule:**
- **Quarterly rebalance** (March, June, September, December 12) — weights reset to equal
- **Semi-annual reconstitution** (March, December 12) — portfolio composition updated
- Between dates, weights are frozen and loaded from the last rebalance

**Index value:**
```
Index(t) = 100 × Σ(weight_i × price_i(t) / price_i(BASE_DATE))
```
Starts at 100 on the base date. Compounds continuously across rebalance periods.

Current base date: **March 12, 2026**

---

## Africa Pivot

This project started with US stocks via the Polygon.io API. The pivot to African markets — starting with NSE Kenya — is deliberate:

- The NSE has 162 listed companies with almost no developer tooling built around it
- Most African retail investors have no programmatic access to clean market data
- The momentum factor is well-documented but has never been applied systematically to NSE
- There is no open, high-frequency, market-based index for Kenya comparable to what exists for US markets

The data provider for the Africa pivot is **mystocks.africa** — a single REST API covering NSE Kenya, JSE (South Africa), NGX (Nigeria), GSE (Ghana), and several other African exchanges. The sandbox environment gives access to live-read-through prices immediately with no approval required.

The ingester rewrite from Polygon to mystocks.africa is minimal — same HTTP client, different base URL and auth header, adjusted ticker format (`SCOM.KE` instead of `SCOM`).

---

## API Endpoints

The Go server exposes a REST API on port `8080`.

### Index

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/index/latest` | Latest momentum index value and daily change |
| `GET` | `/api/index/history` | Historical index values. Query param: `?period=1d\|1w\|1m\|3m\|6m\|ytd\|1y\|all` |
| `GET` | `/api/index/stats` | Summary stats: ATH, ATL, YTD return, 1M return |

### Stocks

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/stocks` | All active stocks in the universe |
| `GET` | `/api/stocks/{ticker}` | Single stock by ticker |
| `GET` | `/api/stocks/{ticker}/history` | Price history. Query param: `?period=1d\|1w\|1m\|3m\|6m\|ytd\|1y\|all` |
| `GET` | `/api/stocks/{ticker}/stats` | Price stats: daily change, 1M return, YTD return, ATH, ATL |

### Rate Limiting

Token bucket algorithm — **60 requests per minute per IP address**. Exceeding the limit returns `HTTP 429` with a `Retry-After: 60` header.

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Daily Pipeline                     │
│                                                     │
│  go run cmd/daily-updater/main.go                   │
│  ├── getLastSavedDate()                             │
│  ├── backfillMissingDates()  ← gap detection        │
│  └── updateAllStocksPrices() ← Polygon / mystocks   │
│                    ↓                                │
│            prices table (Neon DB)                   │
│                    ↓                                │
│  python3 cmd/daily-updater/momentum_index.py        │
│  ├── get_missing_dates()  ← auto date detection     │
│  ├── calculate_momentum_scores()                    │
│  ├── select_portfolio()                             │
│  ├── calculate_weights()                            │
│  └── calculate_index_value()                        │
│                    ↓                                │
│       momentum_index_values table (Neon DB)         │
└─────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────┐
│                    REST API                          │
│                                                     │
│  go run cmd/server/main.go                          │
│  ├── chi router                                     │
│  ├── token bucket rate limiter                      │
│  ├── CORS middleware                                │
│  └── handlers → internal/db/queries.go             │
│                    ↓                                │
│              reads from Neon DB                     │
└─────────────────────────────────────────────────────┘
```

**Database tables:**
- `stocks` — ticker, name, active flag, sector
- `prices` — stock_id, price_date, close_price
- `momentum_index_values` — price_date, index_value, daily_change, momentum_score (JSON), weights (JSON)
- `index_values` — legacy equal-weight index, kept as reference

---

## Running Locally

### Prerequisites

- Go 1.22+
- Python 3.11+
- A Neon (or any PostgreSQL) database
- A market data API key (Polygon.io for US, mystocks.africa for African markets)

### Setup

```bash
git clone https://github.com/koros33/Nse-pulse
cd Nse-pulse
```

Create a `.env` file:
```env
DATABASE_URL=postgresql://user:password@host/dbname?sslmode=require
POLYGON_API_KEY=your_key_here
```

Install Python dependencies:
```bash
pip install -r cmd/daily-updater/requirements.txt
```

Run migrations:
```bash
go run cmd/migrate/main.go
```

### Running the Daily Updater

```bash
# Fetch latest prices and backfill any gaps
go run cmd/daily-updater/main.go

# Calculate momentum index for all missing dates (no arguments needed)
python3 cmd/daily-updater/momentum_index.py

# Or for a specific date
python3 cmd/daily-updater/momentum_index.py 2026-03-12
```

### Running the API Server

```bash
go run cmd/server/main.go
```

Server starts on `http://localhost:8080`.

### Using Your Own Tickers

1. Insert your stocks into the `stocks` table:
```sql
INSERT INTO stocks (ticker, name, active) VALUES
('YOURTICKER', 'Your Company Name', true);
```

2. Set `DATA_START_DATE` in `momentum_index.py` to your earliest available price date.

3. Set `BASE_DATE` to the date you want the index to start at 100.

4. Run the price ingester to backfill historical data, then run the momentum script.

The momentum engine is exchange-agnostic — it reads from your `prices` table regardless of where the data came from. Swap the ingester for any data source and the rest of the pipeline works unchanged.

---

## Future: NSE Kenya Migration

The next phase migrates the data ingester from Polygon.io to **mystocks.africa**, replacing the US stock universe with NSE Kenya equities.

**What changes:**
- Go ingester — new base URL, auth header, and ticker format (`SCOM.KE`)
- `stocks` table — populated with NSE Kenya listings
- `DATA_START_DATE` — set to earliest available NSE history

**What stays the same:**
- Momentum score calculation (Python) — exchange-agnostic
- REST API (Go) — no handler changes
- Database schema — no migrations needed
- Rebalancing logic — identical

**NSE Kenya universe:**
The NSE has 162 listed companies across sectors including banking, manufacturing, energy, telecoms, and agriculture. The initial momentum portfolio will select the top 30 by momentum score, with the option to expand to 50 or 75 stocks at future reconstitution dates.

**Target exchanges (via mystocks.africa):**
- NSE Kenya (primary)
- NGX Nigeria
- JSE South Africa
- GSE Ghana

---

## Stack

| Layer | Technology |
|-------|------------|
| API server | Go, chi router |
| Price ingestion | Go, net/http |
| Momentum engine | Python, pandas, numpy |
| Database | PostgreSQL (Neon) |
| Rate limiting | Token bucket (pure Go, no external deps) |
| Data source (current) | Polygon.io |
| Data source (next) | mystocks.africa |