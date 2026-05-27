-- =============================================================================
-- yindex Migration: Reset to 100-stock index, base date 2026-01-02
-- Run with: psql $DATABASE_URL -f migrate_to_100_stocks.sql
-- =============================================================================

BEGIN;

-- ── Step 1: Wipe 2025 data ────────────────────────────────────────────────────
DELETE FROM index_values
WHERE price_date <= '2025-12-31';

DELETE FROM prices
WHERE price_date <= '2025-12-31';

-- ── Step 2: Deactivate all existing stocks (clean slate) ──────────────────────
UPDATE stocks SET active = FALSE;

-- ── Step 3: Insert 100 stocks (ON CONFLICT re-activates existing ones) ────────
INSERT INTO stocks (ticker, name, active) VALUES
    -- Original 10
    ('AAPL',  'Apple Inc',                         TRUE),
    ('MSFT',  'Microsoft Corporation',              TRUE),
    ('GOOGL', 'Alphabet Inc',                       TRUE),
    ('AMZN',  'Amazon.com Inc',                     TRUE),
    ('META',  'Meta Platforms Inc',                 TRUE),
    ('TSLA',  'Tesla Inc',                          TRUE),
    ('NVDA',  'NVIDIA Corporation',                 TRUE),
    ('JPM',   'JPMorgan Chase',                     TRUE),
    ('V',     'Visa Inc',                           TRUE),
    ('JNJ',   'Johnson & Johnson',                  TRUE),
    -- Tech / Semiconductors
    ('AVGO',  'Broadcom Inc',                       TRUE),
    ('AMD',   'Advanced Micro Devices',             TRUE),
    ('INTC',  'Intel Corporation',                  TRUE),
    ('QCOM',  'Qualcomm Inc',                       TRUE),
    ('TXN',   'Texas Instruments',                  TRUE),
    ('MU',    'Micron Technology',                  TRUE),
    ('AMAT',  'Applied Materials',                  TRUE),
    ('LRCX',  'Lam Research',                       TRUE),
    ('KLAC',  'KLA Corporation',                    TRUE),
    ('MRVL',  'Marvell Technology',                 TRUE),
    -- Software / Cloud
    ('CRM',   'Salesforce Inc',                     TRUE),
    ('ORCL',  'Oracle Corporation',                 TRUE),
    ('NOW',   'ServiceNow Inc',                     TRUE),
    ('ADBE',  'Adobe Inc',                          TRUE),
    ('SNOW',  'Snowflake Inc',                      TRUE),
    ('PLTR',  'Palantir Technologies',              TRUE),
    ('DDOG',  'Datadog Inc',                        TRUE),
    ('ZS',    'Zscaler Inc',                        TRUE),
    ('PANW',  'Palo Alto Networks',                 TRUE),
    ('NET',   'Cloudflare Inc',                     TRUE),
    -- Internet / Consumer Tech
    ('NFLX',  'Netflix Inc',                        TRUE),
    ('UBER',  'Uber Technologies',                  TRUE),
    ('LYFT',  'Lyft Inc',                           TRUE),
    ('ABNB',  'Airbnb Inc',                         TRUE),
    ('SNAP',  'Snap Inc',                           TRUE),
    ('PINS',  'Pinterest Inc',                      TRUE),
    ('SPOT',  'Spotify Technology',                 TRUE),
    ('RBLX',  'Roblox Corporation',                 TRUE),
    ('U',     'Unity Software',                     TRUE),
    ('HOOD',  'Robinhood Markets',                  TRUE),
    -- E-commerce / Retail
    ('SHOP',  'Shopify Inc',                        TRUE),
    ('ETSY',  'Etsy Inc',                           TRUE),
    ('EBAY',  'eBay Inc',                           TRUE),
    ('WMT',   'Walmart Inc',                        TRUE),
    ('TGT',   'Target Corporation',                 TRUE),
    ('COST',  'Costco Wholesale',                   TRUE),
    ('HD',    'Home Depot',                         TRUE),
    ('LOW',   'Lowe\'s Companies',                  TRUE),
    ('NKE',   'Nike Inc',                           TRUE),
    ('LULU',  'Lululemon Athletica',                TRUE),
    -- Financials
    ('GS',    'Goldman Sachs',                      TRUE),
    ('MS',    'Morgan Stanley',                     TRUE),
    ('BAC',   'Bank of America',                    TRUE),
    ('WFC',   'Wells Fargo',                        TRUE),
    ('C',     'Citigroup Inc',                      TRUE),
    ('AXP',   'American Express',                   TRUE),
    ('MA',    'Mastercard Inc',                     TRUE),
    ('BLK',   'BlackRock Inc',                      TRUE),
    ('SCHW',  'Charles Schwab',                     TRUE),
    ('COF',   'Capital One Financial',              TRUE),
    -- Healthcare / Biotech
    ('UNH',   'UnitedHealth Group',                 TRUE),
    ('LLY',   'Eli Lilly and Company',              TRUE),
    ('ABBV',  'AbbVie Inc',                         TRUE),
    ('MRK',   'Merck & Co',                         TRUE),
    ('PFE',   'Pfizer Inc',                         TRUE),
    ('AMGN',  'Amgen Inc',                          TRUE),
    ('GILD',  'Gilead Sciences',                    TRUE),
    ('BIIB',  'Biogen Inc',                         TRUE),
    ('VRTX',  'Vertex Pharmaceuticals',             TRUE),
    ('REGN',  'Regeneron Pharmaceuticals',          TRUE),
    -- Industrials / Aerospace
    ('BA',    'Boeing Company',                     TRUE),
    ('RTX',   'RTX Corporation',                    TRUE),
    ('LMT',   'Lockheed Martin',                    TRUE),
    ('NOC',   'Northrop Grumman',                   TRUE),
    ('GE',    'GE Aerospace',                       TRUE),
    ('CAT',   'Caterpillar Inc',                    TRUE),
    ('DE',    'Deere & Company',                    TRUE),
    ('HON',   'Honeywell International',            TRUE),
    ('MMM',   '3M Company',                         TRUE),
    ('EMR',   'Emerson Electric',                   TRUE),
    -- Energy
    ('XOM',   'Exxon Mobil',                        TRUE),
    ('CVX',   'Chevron Corporation',                TRUE),
    ('COP',   'ConocoPhillips',                     TRUE),
    ('SLB',   'SLB (Schlumberger)',                 TRUE),
    ('EOG',   'EOG Resources',                      TRUE),
    -- Consumer Staples
    ('PG',    'Procter & Gamble',                   TRUE),
    ('KO',    'Coca-Cola Company',                  TRUE),
    ('PEP',   'PepsiCo Inc',                        TRUE),
    ('PM',    'Philip Morris International',        TRUE),
    ('MO',    'Altria Group',                       TRUE),
    -- Telecom / Media
    ('T',     'AT&T Inc',                           TRUE),
    ('VZ',    'Verizon Communications',             TRUE),
    ('TMUS',  'T-Mobile US',                        TRUE),
    ('DIS',   'Walt Disney Company',                TRUE),
    ('WBD',   'Warner Bros Discovery',              TRUE),
    -- REITs / Other
    ('AMT',   'American Tower',                     TRUE),
    ('PLD',   'Prologis Inc',                       TRUE),
    ('EQIX',  'Equinix Inc',                        TRUE),
    ('SPG',   'Simon Property Group',               TRUE),
    ('O',     'Realty Income',                      TRUE)
ON CONFLICT (ticker) DO UPDATE SET
    name   = EXCLUDED.name,
    active = TRUE;

-- ── Step 4: Verify ────────────────────────────────────────────────────────────
DO $$
DECLARE
    stock_count INTEGER;
    price_count INTEGER;
    index_count INTEGER;
BEGIN
    SELECT COUNT(*) INTO stock_count FROM stocks WHERE active = TRUE;
    SELECT COUNT(*) INTO price_count FROM prices WHERE price_date <= '2025-12-31';
    SELECT COUNT(*) INTO index_count FROM index_values WHERE price_date <= '2025-12-31';

    RAISE NOTICE '✅ Active stocks: % (expected 100)', stock_count;
    RAISE NOTICE '✅ 2025 prices remaining: % (expected 0)', price_count;
    RAISE NOTICE '✅ 2025 index values remaining: % (expected 0)', index_count;

    IF stock_count != 100 THEN
        RAISE EXCEPTION '❌ Expected 100 active stocks, got %', stock_count;
    END IF;
    IF price_count != 0 OR index_count != 0 THEN
        RAISE EXCEPTION '❌ 2025 data was not fully deleted';
    END IF;
END $$;

COMMIT;

-- =============================================================================
-- After running this file, backfill 2026 data with:
--   python3 scraper.py --backfill --from 2026-01-02
-- The new base date is 2026-01-02 (first trading day of 2026)
-- =============================================================================