"""
yindex Equal-Weight Index Scraper
Pulls historical + latest closing prices via yfinance and loads to PostgreSQL.
Computes index values starting at 100 from base_date (2025-01-02)

"""

import os
import sys
import logging
from datetime import date, timedelta
from decimal import Decimal

import psycopg2
import psycopg2.extras
import yfinance as yf
import pandas as pd
from dotenv import load_dotenv

load_dotenv()
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger(__name__)

BASE_DATE = date(2025, 1, 2)
BASE_VALUE = Decimal("100")


# ── Database ──────────────────────────────────────────────────────────────────

def get_conn():
    return psycopg2.connect(os.getenv("DATABASE_URL"))


def get_stocks(cur) -> list[dict]:
    cur.execute("SELECT id, ticker, name FROM stocks WHERE active = TRUE ORDER BY id")
    return [{"id": r[0], "ticker": r[1], "name": r[2]} for r in cur.fetchall()]


def upsert_prices(cur, stock_id: int, df: pd.DataFrame):
    """Insert or update daily closing prices for a single stock."""
    # Handle yfinance MultiIndex columns e.g. ('Close', 'AAPL')
    if isinstance(df.columns, pd.MultiIndex):
        df.columns = df.columns.get_level_values(0)

    rows = []
    for idx, row in df.iterrows():
        try:
            close = float(row["Close"])
            if not pd.isna(close):
                rows.append((stock_id, idx.date(), close))
        except (TypeError, ValueError):
            continue

    if not rows:
        return 0

    psycopg2.extras.execute_values(
        cur,
        """
        INSERT INTO prices (stock_id, price_date, close_price)
        VALUES %s
        ON CONFLICT (stock_id, price_date)
        DO UPDATE SET close_price = EXCLUDED.close_price
        """,
        rows,
    )
    return len(rows)


# ── Index Calculation ─────────────────────────────────────────────────────────

def compute_index(cur):
    """
    Equal-weight index formula:
        Index(t) = BASE_VALUE × mean( P(t,i) / P(base,i) )   for all i stocks

    We only compute dates where ALL 10 stocks have a price.
    """
    cur.execute("""
        SELECT p.price_date, s.ticker, p.close_price
        FROM prices p
        JOIN stocks s ON s.id = p.stock_id
        WHERE s.active = TRUE
          AND p.price_date >= %s
        ORDER BY p.price_date
    """, (BASE_DATE,))
    rows = cur.fetchall()
    if not rows:
        log.warning("No price data found from base date onwards.")
        return

    df = pd.DataFrame(rows, columns=["price_date", "ticker", "close_price"])
    df["close_price"] = df["close_price"].astype(float)

    pivot = df.pivot(index="price_date", columns="ticker", values="close_price")
    n_stocks = pivot.shape[1]

    # Only keep dates where all stocks have prices
    pivot = pivot.dropna(thresh=n_stocks)
    if pivot.empty:
        log.warning("No dates with complete price coverage across all stocks.")
        return

    # Base prices — first available date on or after BASE_DATE
    base_row = pivot.iloc[0]
    log.info(f"Base date for index: {pivot.index[0]}  |  stocks: {list(pivot.columns)}")

    # Compute normalised returns and equal-weight index
    ratios = pivot.div(base_row)                     # P(t) / P(base)
    index_series = ratios.mean(axis=1) * float(BASE_VALUE)

    # Daily % change
    pct_change = index_series.pct_change() * 100

    index_rows = [
        (str(d), round(float(v), 4), round(float(pct_change[d]), 4) if pd.notna(pct_change[d]) else None)
        for d, v in index_series.items()
    ]

    psycopg2.extras.execute_values(
        cur,
        """
        INSERT INTO index_values (price_date, index_value, daily_change)
        VALUES %s
        ON CONFLICT (price_date)
        DO UPDATE SET
            index_value  = EXCLUDED.index_value,
            daily_change = EXCLUDED.daily_change
        """,
        index_rows,
    )
    log.info(f"Index computed for {len(index_rows)} trading days.")
    log.info(f"Latest index value: {index_rows[-1][1]:.2f}  ({index_rows[-1][0]})")


# ── Fetching ──────────────────────────────────────────────────────────────────

def fetch_stock(ticker: str, start: date, end: date) -> pd.DataFrame:
    log.info(f"Fetching {ticker}  {start} → {end}")
    try:
        df = yf.download(
            ticker,
            start=str(start),
            end=str(end + timedelta(days=1)),  # yfinance end is exclusive
            progress=False,
            auto_adjust=True,
        )
        if df.empty:
            log.warning(f"  No data returned for {ticker}")
        return df
    except Exception as e:
        log.error(f"  Failed to fetch {ticker}: {e}")
        return pd.DataFrame()


def last_stored_date(cur, stock_id: int) -> date | None:
    cur.execute(
        "SELECT MAX(price_date) FROM prices WHERE stock_id = %s", (stock_id,)
    )
    result = cur.fetchone()[0]
    return result  # None if no data yet


# ── Main ──────────────────────────────────────────────────────────────────────

def run(backfill: bool = False):
    conn = get_conn()
    try:
        with conn:
            with conn.cursor() as cur:
                stocks = get_stocks(cur)
                log.info(f"Found {len(stocks)} active stocks.")

                today = date.today()

                for stock in stocks:
                    if backfill:
                        start = BASE_DATE
                    else:
                        last = last_stored_date(cur, stock["id"])
                        start = (last + timedelta(days=1)) if last else BASE_DATE

                    if start > today:
                        log.info(f"{stock['ticker']} is up to date.")
                        continue

                    df = fetch_stock(stock["ticker"], start, today)
                    if df.empty:
                        continue

                    n = upsert_prices(cur, stock["id"], df)
                    log.info(f"  → {n} rows saved for {stock['ticker']}")

                log.info("Recomputing index values...")
                compute_index(cur)

        log.info("All done.")
    finally:
        conn.close()


if __name__ == "__main__":
    backfill = "--backfill" in sys.argv
    if backfill:
        log.info("Running in BACKFILL mode — pulling all history from base date.")
    run(backfill=backfill) 