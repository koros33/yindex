#!/usr/bin/env python3
"""
MSCI-Style Momentum Index Calculator
- Quarterly rebalancing (weights updated every 3 months)
- Semi-annual reconstitution (stock selection every 6 months)
- Base date: June 12, 2026 (index starts at 100)
- Universe: active stocks in DB (your S&P 500 subset)
"""
from dotenv import load_dotenv
import os
import json
import numpy as np
import pandas as pd
import psycopg2
import sys
from datetime import datetime, date
from typing import List, Dict, Optional

load_dotenv()

DATABASE_URL    = os.getenv("DATABASE_URL")
BASE_DATE      = "2026-03-12"       # index starts at 100
DATA_START_DATE = "2024-06-07"       # how far back to pull prices for momentum calc

# Quarterly rebalance dates (weights only)
REBALANCE_DATES = [
    "2026-03-12", "2026-06-12", "2026-09-12", "2026-12-12",
    "2027-03-12", "2027-06-12", "2027-09-12", "2027-12-12",
]

# Semi-annual reconstitution dates (change which stocks are in)
RECONSTITUTION_DATES = [
    "2026-03-12", "2026-06-12", "2026-12-12",
    "2027-06-12", "2027-12-12",
]

# How many stocks to hold
PORTFOLIO_SIZE = 30


class MomentumIndex:
    def __init__(self):
        self.portfolio: List[str] = []      # current stocks in index
        self.frozen_weights: Optional[pd.Series] = None  # weights between rebalances
        self.last_rebalance_date: Optional[str] = None

    # ── DB ────────────────────────────────────────────────────────────────────

    def _connect(self):
        return psycopg2.connect(DATABASE_URL)

    def get_universe(self) -> List[str]:
        """All active tickers from DB (your S&P 500 subset)"""
        with self._connect() as conn:
            df = pd.read_sql(
                "SELECT ticker FROM stocks WHERE active = TRUE ORDER BY ticker",
                conn
            )
        return df["ticker"].tolist()

    def fetch_prices(self, tickers: List[str], from_date: str) -> Dict[str, pd.Series]:
        """Fetch historical close prices from DB"""
        query = """
            SELECT s.ticker, p.price_date, p.close_price
            FROM prices p
            JOIN stocks s ON s.id = p.stock_id
            WHERE s.ticker = ANY(%s)
              AND p.price_date >= %s
            ORDER BY s.ticker, p.price_date
        """
        with self._connect() as conn:
            df = pd.read_sql(query, conn, params=(tickers, from_date))

        prices = {}
        for ticker in tickers:
            t = df[df["ticker"] == ticker]
            if len(t) > 0:
                prices[ticker] = t.set_index("price_date")["close_price"]
        return prices

    def get_base_prices(self, tickers: List[str]) -> Dict[str, float]:
        """Prices on BASE_DATE for index normalization"""
        query = """
            SELECT s.ticker, p.close_price
            FROM prices p
            JOIN stocks s ON s.id = p.stock_id
            WHERE s.ticker = ANY(%s)
              AND p.price_date = %s
        """
        with self._connect() as conn:
            df = pd.read_sql(query, conn, params=(tickers, BASE_DATE))
        return df.set_index("ticker")["close_price"].to_dict()

    def load_frozen_weights(self, rebalance_date: str) -> Optional[pd.Series]:
        """Load saved weights from a past rebalance date"""
        query = """
            SELECT weights FROM momentum_index_values
            WHERE price_date = %s AND weights IS NOT NULL
            LIMIT 1
        """
        try:
            with self._connect() as conn:
                with conn.cursor() as cur:
                    cur.execute(query, (rebalance_date,))
                    row = cur.fetchone()
                    if row and row[0]:
                        return pd.Series(json.loads(row[0]))
        except Exception as e:
            print(f"⚠️ Could not load weights for {rebalance_date}: {e}")
        return None

    def load_frozen_portfolio(self, reconstitution_date: str) -> Optional[List[str]]:
        """Load saved portfolio (tickers) from a past reconstitution date"""
        query = """
            SELECT momentum_score FROM momentum_index_values
            WHERE price_date = %s AND momentum_score IS NOT NULL
            LIMIT 1
        """
        try:
            with self._connect() as conn:
                with conn.cursor() as cur:
                    cur.execute(query, (reconstitution_date,))
                    row = cur.fetchone()
                    if row and row[0]:
                        scores = json.loads(row[0])
                        # return tickers sorted by score descending
                        return sorted(scores, key=scores.get, reverse=True)[:PORTFOLIO_SIZE]
        except Exception:
            pass
        return None

    def save_daily(
        self,
        price_date: str,
        index_value: float,
        daily_change: float,
        scores: pd.Series,
        weights: pd.Series,
    ):
        query = """
            INSERT INTO momentum_index_values
                (price_date, index_value, daily_change, momentum_score, weights)
            VALUES (%s, %s, %s, %s, %s)
            ON CONFLICT (price_date) DO UPDATE
                SET index_value    = EXCLUDED.index_value,
                    daily_change   = EXCLUDED.daily_change,
                    momentum_score = EXCLUDED.momentum_score,
                    weights        = EXCLUDED.weights,
                    updated_at     = NOW()
        """
        with self._connect() as conn:
            with conn.cursor() as cur:
                cur.execute(query, (
                    price_date,
                    float(index_value),
                    float(daily_change),
                    scores.to_json(),
                    weights.to_json(),
                ))
            conn.commit()

    # ── Math ──────────────────────────────────────────────────────────────────

    def calculate_momentum_scores(self, prices: Dict[str, pd.Series]) -> pd.Series:
        """
        Calculate momentum scores for each ticker as of the LATEST date.
        Returns a Series: ticker -> score
        """
        df = pd.DataFrame(prices).sort_index()

        if len(df) < 365:
            print(f"⚠️  Only {len(df)} days of data, need 365")
            return pd.Series(dtype=float)

        # Latest row only (we score as of today)
        latest = df.iloc[-1]
        row_365 = df.iloc[-365] if len(df) >= 365 else None
        row_180 = df.iloc[-180] if len(df) >= 180 else None
        row_shift1 = df.iloc[-2]   # skip most recent day (standard)

        momentum_12m = row_shift1 / df.iloc[-365] - 1
        momentum_6m  = row_shift1 / df.iloc[-180] - 1
        combined     = 0.5 * momentum_12m + 0.5 * momentum_6m

        # Risk-adjust by 1Y return volatility
        returns   = df.pct_change().dropna()
        vol       = returns.tail(365).std() * np.sqrt(252)
        vol       = vol.replace(0, np.nan)
        risk_adj  = combined / vol

        # Winsorize at 5/95
        low, high = risk_adj.quantile(0.05), risk_adj.quantile(0.95)
        winsorized = risk_adj.clip(lower=low, upper=high)

        # Z-score
        z = (winsorized - winsorized.mean()) / winsorized.std()

        # MSCI non-linear transform
        score = z.apply(lambda x: 1 + x if x > 0 else 1 / (1 - x))

        return score.dropna().sort_values(ascending=False)

    def select_portfolio(self, scores: pd.Series) -> List[str]:
        """Top N tickers by momentum score"""
        return scores.head(PORTFOLIO_SIZE).index.tolist()

    def calculate_weights(self, scores: pd.Series, portfolio: List[str]) -> pd.Series:
        """Equal weight within selected portfolio"""
        subset = scores[portfolio]
        return (subset / subset.sum()).reindex(portfolio)

    def calculate_index_value(
        self,
        prices: Dict[str, pd.Series],
        weights: pd.Series,
        base_prices: Dict[str, float],
    ) -> pd.Series:
        """
        Index value for every date using FROZEN weights.
        Index(t) = 100 * Σ(weight_i * price_i(t) / price_i(BASE_DATE))
        """
        df = pd.DataFrame(prices).sort_index()
        base = pd.Series(base_prices)

        # Only keep tickers we have base prices for
        common = [t for t in weights.index if t in base.index and t in df.columns]
        w = weights[common]
        w = w / w.sum()   # renormalize after filtering

        relatives = df[common].div(base[common], axis=1)
        index     = 100 * (relatives * w).sum(axis=1)
        return index

    # ── Orchestration ─────────────────────────────────────────────────────────

    def get_active_rebalance_date(self, target_date: str) -> str:
        """Most recent rebalance date <= target_date"""
        past = [d for d in REBALANCE_DATES if d <= target_date]
        return max(past) if past else REBALANCE_DATES[0]

    def get_active_reconstitution_date(self, target_date: str) -> str:
        """Most recent reconstitution date <= target_date"""
        past = [d for d in RECONSTITUTION_DATES if d <= target_date]
        return max(past) if past else RECONSTITUTION_DATES[0]

    def run(self, target_date: Optional[str] = None):
        target_date = target_date or datetime.now().strftime("%Y-%m-%d")
        print(f"\n📊 Momentum Index — {target_date}")

        active_rebal   = self.get_active_rebalance_date(target_date)
        active_reconst = self.get_active_reconstitution_date(target_date)
        is_rebalance      = target_date == active_rebal
        is_reconstitution = target_date == active_reconst

        print(f"   Active rebalance date:      {active_rebal}")
        print(f"   Active reconstitution date: {active_reconst}")
        print(f"   Is rebalance day:           {is_rebalance}")
        print(f"   Is reconstitution day:      {is_reconstitution}")

        # ── Step 1: Get universe ──────────────────────────────────────────────
        universe = self.get_universe()
        print(f"   Universe: {len(universe)} stocks")

        # ── Step 2: Fetch all prices ──────────────────────────────────────────
        print("📥 Fetching prices from DB...")
        prices = self.fetch_prices(universe, DATA_START_DATE)
        # Trim prices to target_date so index calc is point-in-time accurate
        cutoff = pd.Timestamp(target_date).normalize()
        prices = {t: s[pd.to_datetime(s.index) <= cutoff] for t, s in prices.items()}
        print(f"✅ Got prices for {len(prices)} stocks")

        # ── Step 3: Calculate momentum scores (always fresh) ─────────────────
        print("🧮 Calculating momentum scores...")
        scores = self.calculate_momentum_scores(prices)
        if scores.empty:
            print("❌ Insufficient data")
            return

        # ── Step 4: Reconstitution (semi-annual) ─────────────────────────────
        if is_reconstitution:
            print(f"🔄 RECONSTITUTION — selecting top {PORTFOLIO_SIZE} from universe")
            portfolio = self.select_portfolio(scores)
            print(f"   Portfolio: {portfolio}")
        else:
            # Load portfolio from last reconstitution date
            portfolio = self.load_frozen_portfolio(active_reconst)
            if not portfolio:
                print(f"   No saved portfolio for {active_reconst}, selecting fresh")
                portfolio = self.select_portfolio(scores)
            print(f"   Using frozen portfolio ({len(portfolio)} stocks) from {active_reconst}")

        # ── Step 5: Rebalance (quarterly) ────────────────────────────────────
        if is_rebalance or is_reconstitution:
            print(f"⚖️  REBALANCE — recalculating weights")
            weights = self.calculate_weights(scores, portfolio)
        else:
            # Load frozen weights from last rebalance date
            weights = self.load_frozen_weights(active_rebal)
            if weights is None:
                print(f"   No saved weights for {active_rebal}, calculating fresh")
                weights = self.calculate_weights(scores, portfolio)
            print(f"   Using frozen weights from {active_rebal}")

        # ── Step 6: Calculate index value ────────────────────────────────────
        base_prices = self.get_base_prices(portfolio)
        index_series = self.calculate_index_value(prices, weights, base_prices)

        index_series.index = pd.to_datetime(index_series.index).normalize()

        if index_series.empty:
            print("❌ Could not calculate index")
            return

        # Only save today's value
        print(f"📅 Target date requested:        {target_date}")
        print(f"📅 Last date in index_series:     {index_series.index[-1]}")

        today_str = pd.Timestamp(target_date).normalize()
        if today_str not in index_series.index:
            print(f"⚠️  {target_date} NOT FOUND in calculated index series.")
            print(f"⚠️  Available dates near target: {[str(d) for d in index_series.index[-5:]]}")
            print(f"⛔ Refusing to save under a mismatched date. Aborting.")
            return
        else:
            today_value = index_series[today_str]

        # Force base date to exactly 100
        if target_date == BASE_DATE:
            today_value = 100.0
            daily_change = 0.0
        else:
            # Daily change
            prev_values = index_series[index_series.index < today_str]
            daily_change = today_value - prev_values.iloc[-1] if len(prev_values) > 0 else 0.0

        # ── Step 7: Save ─────────────────────────────────────────────────────
        # Force base date index value to exactly 100
        if target_date == BASE_DATE:
            today_value = 100.0
            daily_change = 0.0

        print("💾 Saving to DB...")
        self.save_daily(
            price_date   = str(today_str.date()) if hasattr(today_str, 'date') else target_date,
            index_value  = today_value,
            daily_change = daily_change,
            scores       = scores[portfolio],
            weights      = weights,
        )
        print(f"\n✅ Done!")
        print(f"📊 Index value:  {today_value:.2f}")
        print(f"📈 Daily change: {daily_change:+.4f}")
        print(f"⚖️  Portfolio:   {portfolio[:5]}... ({len(portfolio)} stocks)")

def get_missing_dates(idx) -> list:
    """Find dates in prices not yet calculated in momentum_index_values"""
    query = """
        SELECT DISTINCT p.price_date
        FROM prices p
        JOIN stocks s ON s.id = p.stock_id
        WHERE s.active = TRUE
          AND p.price_date > (
              SELECT COALESCE(MAX(price_date), '2026-03-12')
              FROM momentum_index_values
          )
        ORDER BY p.price_date ASC
    """
    with idx._connect() as conn:
        df = pd.read_sql(query, conn)
    return [str(d.date()) if hasattr(d, 'date') else str(d)
            for d in df["price_date"].tolist()]


def main():
    idx = MomentumIndex()

    if len(sys.argv) > 1:
        # explicit date passed
        idx.run(sys.argv[1])
    else:
        # auto-detect missing dates
        missing = get_missing_dates(idx)
        if not missing:
            print("✅ Momentum index already up to date")
            return
        print(f"📅 Found {len(missing)} dates to calculate: {missing[0]} → {missing[-1]}")
        for d in missing:
            print(f"\n--- {d} ---")
            idx.run(d)
        print("\n✅ All dates calculated")

if __name__ == "__main__":
    main()