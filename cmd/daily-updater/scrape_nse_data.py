"""
scrape_nse_data.py

Purpose:
    Populate the 41-stock NSE dividend-screen CSV from Kwayisi.

Scraped from Kwayisi:
    current_price
    annual_dividend (Dividend Per Share)
    eps
    pe_ratio
    dividend_yield_source

Already supplied manually in CSV:
    shares_outstanding
    dividend_years_paid

Calculated locally:
    market_cap = current_price * shares_outstanding
    payout_ratio = annual_dividend / eps

IMPORTANT:
    We deliberately DO NOT scrape:
        - Shares Outstanding
        - Market Capitalization

    Shares outstanding is taken from the CSV because you entered it
    manually, and market cap is calculated from price × shares.

Run locally:
    python scrape_nse_data.py nse_dividend_screen.csv
"""

import re
import sys
import time
import datetime
import requests
from bs4 import BeautifulSoup
import pandas as pd


HEADERS = {
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/124.0 Safari/537.36"
    )
}

REQUEST_DELAY_SECONDS = 2


LABELS = {
    "eps": "Earnings Per Share",
    "pe_ratio": "Price/Earning Ratio",
    "dividend_per_share": "Dividend Per Share",
    "dividend_yield": "Dividend Yield",
}


NUMERIC_RE = re.compile(r"-?[\d,]+(?:\.\d+)?")


def clean_number(text):
    """
    Convert values such as:

        'KES 3.33'
        '3.33'
        '18.6B'
        '1.2M'
        '5.43%'

    into floats.
    """

    if text is None:
        return None

    text = str(text).strip()

    if not text:
        return None

    multiplier = 1

    if text.endswith(("B", "b")):
        multiplier = 1_000_000_000
        text = text[:-1]

    elif text.endswith(("M", "m")):
        multiplier = 1_000_000
        text = text[:-1]

    match = NUMERIC_RE.search(text.replace(",", ""))

    if not match:
        return None

    try:
        return float(match.group()) * multiplier
    except ValueError:
        return None


def value_after_label(soup, label_text):
    """
    Find a Kwayisi label and retrieve the value immediately associated
    with it.

    We deliberately search by label text rather than CSS class because
    the page structure may change.
    """

    element = soup.find(
        string=lambda s: s and s.strip() == label_text
    )

    if element is None:
        return None

    node = element.parent

    # ---------------------------------------------------------
    # 1. Label/value in table cells
    # ---------------------------------------------------------

    if node and node.name in ("td", "th"):

        sibling = node.find_next_sibling(["td", "th"])

        if sibling:
            value = sibling.get_text(" ", strip=True)

            if value:
                return value

    # ---------------------------------------------------------
    # 2. Label and value as sibling elements
    # ---------------------------------------------------------

    if node:

        sibling = node.find_next_sibling()

        if sibling:

            value = sibling.get_text(" ", strip=True)

            if value:
                return value

    # ---------------------------------------------------------
    # 3. Label/value separated by parent structure
    # ---------------------------------------------------------

    if node and node.parent:

        sibling = node.parent.find_next_sibling()

        if sibling:

            value = sibling.get_text(" ", strip=True)

            if value:
                return value

    return None


def extract_price(soup, ticker):
    """
    Extract the price displayed next to the ticker in the Kwayisi
    header.
    """

    ticker = ticker.upper()

    price_element = soup.find(
        string=lambda s: (
            s and s.strip().startswith(ticker)
        )
    )

    if price_element:

        text = price_element.strip()

        # Remove ticker from beginning
        remaining = text[len(ticker):]

        price = clean_number(remaining)

        if price is not None:
            return price

    return None


def fetch_kwayisi(ticker):
    """
    Download one Kwayisi page and extract only the fields we need.

    IMPORTANT:
        We do NOT scrape shares outstanding.
        We do NOT scrape market cap.
    """

    url = (
        f"https://afx.kwayisi.org/nse/"
        f"{ticker.lower()}.html"
    )

    result = {
        "source_url": url
    }

    try:

        response = requests.get(
            url,
            headers=HEADERS,
            timeout=20
        )

        response.raise_for_status()

        soup = BeautifulSoup(
            response.text,
            "html.parser"
        )

        # -----------------------------------------------------
        # PRICE
        # -----------------------------------------------------

        price = extract_price(
            soup,
            ticker
        )

        if price is not None:
            result["current_price"] = price

        # -----------------------------------------------------
        # EPS
        # -----------------------------------------------------

        eps_raw = value_after_label(
            soup,
            LABELS["eps"]
        )

        if eps_raw:
            result["eps"] = clean_number(eps_raw)

        # -----------------------------------------------------
        # DIVIDEND PER SHARE
        # -----------------------------------------------------

        dps_raw = value_after_label(
            soup,
            LABELS["dividend_per_share"]
        )

        if dps_raw:
            result["annual_dividend"] = clean_number(
                dps_raw
            )

        # -----------------------------------------------------
        # P/E
        # -----------------------------------------------------

        pe_raw = value_after_label(
            soup,
            LABELS["pe_ratio"]
        )

        if pe_raw:
            result["pe_ratio"] = clean_number(
                pe_raw
            )

        # -----------------------------------------------------
        # DIVIDEND YIELD
        # -----------------------------------------------------

        yield_raw = value_after_label(
            soup,
            LABELS["dividend_yield"]
        )

        if yield_raw:
            result["dividend_yield_source"] = clean_number(
                yield_raw
            )

        # -----------------------------------------------------
        # DEBUG OUTPUT
        # -----------------------------------------------------

        print(
            f"    price={result.get('current_price')}, "
            f"DPS={result.get('annual_dividend')}, "
            f"EPS={result.get('eps')}, "
            f"P/E={result.get('pe_ratio')}, "
            f"yield={result.get('dividend_yield_source')}"
        )

    except requests.RequestException as error:

        print(
            f"    [{ticker}] REQUEST ERROR: {error}"
        )

    except Exception as error:

        print(
            f"    [{ticker}] PARSING ERROR: {error}"
        )

    return result


def parse_shares(value):
    """
    Convert manually entered shares outstanding into a number.

    Handles values such as:

        3,213,462,815
        3213462815
        3.21B
    """

    if pd.isna(value):
        return None

    return clean_number(value)


def calculate_fields(df, row_index):
    """
    Calculate values that we do NOT scrape.
    """

    price = pd.to_numeric(
        df.at[row_index, "current_price"],
        errors="coerce"
    )

    shares = parse_shares(
        df.at[row_index, "shares_outstanding"]
    )

    eps = pd.to_numeric(
        df.at[row_index, "eps"],
        errors="coerce"
    )

    dividend = pd.to_numeric(
        df.at[row_index, "annual_dividend"],
        errors="coerce"
    )

    # ---------------------------------------------------------
    # MARKET CAP
    #
    # Market cap = share price × shares outstanding
    # ---------------------------------------------------------

    if pd.notna(price) and shares is not None:

        market_cap = price * shares

        df.at[
            row_index,
            "market_cap"
        ] = market_cap

    # ---------------------------------------------------------
    # PAYOUT RATIO
    #
    # Dividend payout ratio = DPS / EPS
    # ---------------------------------------------------------

    if (
        pd.notna(dividend)
        and pd.notna(eps)
        and eps != 0
    ):

        payout_ratio = dividend / eps

        df.at[
            row_index,
            "payout_ratio"
        ] = payout_ratio


def scrape_all(csv_path):

    # ---------------------------------------------------------
    # LOAD CSV
    # ---------------------------------------------------------

    df = pd.read_csv(csv_path)

    print(
        f"\nLoaded {len(df)} companies."
    )

    # ---------------------------------------------------------
    # MAKE SURE NUMERIC COLUMNS EXIST
    # ---------------------------------------------------------

    numeric_columns = [
        "current_price",
        "annual_dividend",
        "eps",
        "market_cap",
        "pe_ratio",
        "dividend_yield_source",
        "payout_ratio",
    ]

    for column in numeric_columns:

        if column not in df.columns:
            df[column] = pd.NA

    # ---------------------------------------------------------
    # PROCESS EACH COMPANY
    # ---------------------------------------------------------

    for i, row in df.iterrows():

        ticker = str(row["ticker"]).strip()

        company = row["company_name"]

        print(
            f"\n[{i + 1}/{len(df)}] "
            f"{ticker} — {company}"
        )

        # -----------------------------------------------------
        # FETCH KWAYISI DATA
        # -----------------------------------------------------

        data = fetch_kwayisi(ticker)

        # -----------------------------------------------------
        # WRITE SCRAPED DATA
        # -----------------------------------------------------

        for key, value in data.items():

            if (
                value is not None
                and value != ""
            ):

                df.at[i, key] = value

        # -----------------------------------------------------
        # CALCULATE MARKET CAP + PAYOUT RATIO
        # -----------------------------------------------------

        calculate_fields(
            df,
            i
        )

        # -----------------------------------------------------
        # UPDATE DATE
        # -----------------------------------------------------

        df.at[
            i,
            "last_updated"
        ] = datetime.date.today().isoformat()

        # -----------------------------------------------------
        # POLITE DELAY
        # -----------------------------------------------------

        time.sleep(
            REQUEST_DELAY_SECONDS
        )

    # ---------------------------------------------------------
    # SAVE
    # ---------------------------------------------------------

    df.to_csv(
        csv_path,
        index=False
    )

    print(
        f"\nDone. Updated {csv_path}"
    )

    print(
        "\nNOTE:"
    )

    print(
        "shares_outstanding was NOT scraped."
    )

    print(
        "market_cap was CALCULATED as "
        "current_price × shares_outstanding."
    )

    print(
        "dividend_years_paid was NOT modified."
    )


if __name__ == "__main__":

    target = (
        sys.argv[1]
        if len(sys.argv) > 1
        else "nse_dividend_screen.csv"
    )

    scrape_all(target)