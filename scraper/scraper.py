"""
yindex Equal-Weight Index Scraper
Pulls historical + latest closing prices via yfinance and loads to PostgreSQL.
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

# Load .env ONLY for local development
if os.getenv("GITHUB_ACTIONS") != "true":
    load_dotenv()

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s"
)

log = logging.getLogger(__name__)

BASE_DATE = date(2025, 1, 2)
BASE_VALUE = Decimal("100")


# ─────────────────────────────────────────────────────────────
# DATABASE
# ─────────────────────────────────────────────────────────────

def get_conn():
    db_url = os.getenv("DATABASE_URL")

    if not db_url:
        raise ValueError("DATABASE_URL is missing")

    log.info("Connecting to PostgreSQL...")

    return psycopg2.connect(
        db_url,
        sslmode="require"
    )


def get_stocks(cur):
    cur.execute("""
        SELECT id, ticker, name
        FROM stocks
        WHERE active = TRUE
        ORDER BY id
    """)

    return [
        {
            "id": row[0],
            "ticker": row[1],
            "name": row[2]
        }
        for row in cur.fetchall()
    ]


def last_stored_date(cur, stock_id):
    cur.execute(
        """
        SELECT MAX(price_date)
        FROM prices
        WHERE stock_id = %s
        """,
        (stock_id,)
    )

    return cur.fetchone()[0]


def upsert_prices(cur, stock_id, df):
    if isinstance(df.columns, pd.MultiIndex):
        df.columns = df.columns.get_level_values(0)

    rows = []

    for idx, row in df.iterrows():
        try:
            close = float(row["Close"])

            if not pd.isna(close):
                rows.append(
                    (
                        stock_id,
                        idx.date(),
                        close
                    )
                )

        except Exception:
            continue

    if not rows:
        return 0

    psycopg2.extras.execute_values(
        cur,
        """
        INSERT INTO prices (
            stock_id,
            price_date,
            close_price
        )
        VALUES %s
        ON CONFLICT (stock_id, price_date)
        DO UPDATE SET
            close_price = EXCLUDED.close_price
        """,
        rows
    )

    return len(rows)


# ─────────────────────────────────────────────────────────────
# INDEX CALCULATION
# ─────────────────────────────────────────────────────────────

def compute_index(cur):

    cur.execute("""
        SELECT
            p.price_date,
            s.ticker,
            p.close_price
        FROM prices p
        JOIN stocks s
            ON s.id = p.stock_id
        WHERE s.active = TRUE
          AND p.price_date >= %s
        ORDER BY p.price_date
    """, (BASE_DATE,))

    rows = cur.fetchall()

    if not rows:
        log.warning("No price data found.")
        return

    df = pd.DataFrame(
        rows,
        columns=[
            "price_date",
            "ticker",
            "close_price"
        ]
    )

    df["close_price"] = df["close_price"].astype(float)

    pivot = df.pivot(
        index="price_date",
        columns="ticker",
        values="close_price"
    )

    n_stocks = pivot.shape[1]

    pivot = pivot.dropna(thresh=n_stocks)

    if pivot.empty:
        log.warning("No complete trading days.")
        return

    base_row = pivot.iloc[0]

    ratios = pivot.div(base_row)

    index_series = ratios.mean(axis=1) * float(BASE_VALUE)

    pct_change = index_series.pct_change() * 100

    rows_to_insert = []

    for d, v in index_series.items():

        daily_change = pct_change[d]

        rows_to_insert.append(
            (
                str(d),
                round(float(v), 4),
                round(float(daily_change), 4)
                if pd.notna(daily_change)
                else None
            )
        )

    psycopg2.extras.execute_values(
        cur,
        """
        INSERT INTO index_values (
            price_date,
            index_value,
            daily_change
        )
        VALUES %s
        ON CONFLICT (price_date)
        DO UPDATE SET
            index_value = EXCLUDED.index_value,
            daily_change = EXCLUDED.daily_change
        """,
        rows_to_insert
    )

    log.info(f"Computed {len(rows_to_insert)} index values.")
    log.info(f"Latest index: {rows_to_insert[-1][1]}")


# ─────────────────────────────────────────────────────────────
# FETCHING
# ─────────────────────────────────────────────────────────────

def fetch_stock(ticker, start, end):

    log.info(f"Fetching {ticker}: {start} → {end}")

    try:
        df = yf.download(
            ticker,
            start=str(start),
            end=str(end + timedelta(days=1)),
            progress=False,
            auto_adjust=True
        )

        if df.empty:
            log.warning(f"No data for {ticker}")

        return df

    except Exception as e:
        log.error(f"Failed fetching {ticker}: {e}")
        return pd.DataFrame()


# ─────────────────────────────────────────────────────────────
# MAIN
# ─────────────────────────────────────────────────────────────

def run(backfill=False):

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
                        last = last_stored_date(
                            cur,
                            stock["id"]
                        )

                        start = (
                            last + timedelta(days=1)
                        ) if last else BASE_DATE

                    if start > today:
                        log.info(
                            f"{stock['ticker']} already up to date."
                        )
                        continue

                    df = fetch_stock(
                        stock["ticker"],
                        start,
                        today
                    )

                    if df.empty:
                        continue

                    n = upsert_prices(
                        cur,
                        stock["id"],
                        df
                    )

                    log.info(
                        f"Saved {n} rows for {stock['ticker']}"
                    )

                log.info("Recomputing index...")

                compute_index(cur)

        log.info("Finished successfully.")

    finally:
        conn.close()


if __name__ == "__main__":

    backfill = "--backfill" in sys.argv

    if backfill:
        log.info("Running BACKFILL mode.")

    run(backfill=backfill)