# yindex 📈

An equal-weight stock index tracker built with Python, PostgreSQL and Go.
Fetches daily closing prices via yfinance, computes a normalised index starting at 100, and exposes it via a REST API.

> **Current index value: 131.95** — up ~32% since January 2025 baseline.

---

## How it works

```
yfinance → scraper.py → Neon PostgreSQL → Go REST API → UI
```

- **Equal-weight formula:** `Index(t) = 100 × mean( P(t,i) / P(base,i) )` across all 10 stocks
- **Base date:** 2025-01-02 = 100
- **Universe:** 10 large-cap US stocks (AAPL, MSFT, GOOGL, AMZN, META, TSLA, NVDA, JPM, V, JNJ)
- **Updates:** Daily cron job runs at US market close (21:00 UTC)

---

## Stack

| Layer | Technology |
|---|---|
| Data fetching | Python + yfinance |
| Database | PostgreSQL (Neon) |
| API | Go (net/http) |
| Tests | pytest |
| CI | GitHub Actions |

---

## Project structure

```
yindex/
├── scraper/
│   ├── scraper.py            # fetch prices + compute index
│   ├── requirements.txt
│   └── tests/
│       └── test_nairobi_pulse.py
├── server/
│   └── main.go               # REST API
├── db/
│   └── schema.sql            # PostgreSQL schema
├── .env.example              # environment variable template
├── .gitignore
└── README.md
```

---

## Getting started

**1. Clone and set up environment:**
```bash
git clone https://github.com/koros33/yindex.git
cd yindex
cp .env.example .env
# fill in your Neon DB credentials
```

**2. Install Python dependencies:**
```bash
cd scraper
pip install -r requirements.txt
```

**3. Run schema:**
```bash
psql $DATABASE_URL -f db/schema.sql
```

**4. Backfill historical data:**
```bash
python3 scraper.py --backfill
```

**5. Run daily update:**
```bash
python3 scraper.py
```

---

## Tests

```bash
cd scraper
pytest tests/test_nairobi_pulse.py -v -s
```

```
✅ test_returns_data
✅ test_has_close_column
✅ test_at_least_four_days
✅ test_positive_prices

4 passed in 1.69s
```

---

## Cron job (daily at US market close)

Runs every weekday at 21:00 UTC (4PM EST) via GitHub Actions:

```yaml
on:
  schedule:
    - cron: '0 21 * * 1-5'
```

---

## Notes

- yfinance has limited coverage for emerging market exchanges (NSE Kenya etc) — this project uses US large-caps as a reliable data source
- Production version can be extended to pull from direct exchange feeds
- SSL required for all Neon DB connections

---

## Author

Built by [@koros33](https://github.com/koros33) 🇰🇪