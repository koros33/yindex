-- +goose Up
-- migrations/000001_add_momentum_index_table.up.sql

-- Create momentum index values table
CREATE TABLE IF NOT EXISTS momentum_index_values (
    id BIGSERIAL PRIMARY KEY,
    price_date DATE UNIQUE NOT NULL,
    index_value DECIMAL(18, 6) NOT NULL,
    daily_change DECIMAL(18, 6),
    momentum_score JSONB,  -- Store raw scores for each stock
    weights JSONB,          -- Store weights for each stock
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Create index on price_date for fast queries
CREATE INDEX IF NOT EXISTS idx_momentum_price_date ON momentum_index_values(price_date);

-- Add active column to stocks table (if not exists)
ALTER TABLE stocks ADD COLUMN IF NOT EXISTS active BOOLEAN DEFAULT TRUE;

-- Update existing stocks to active
UPDATE stocks SET active = TRUE WHERE active IS NULL;

-- Create index_values table for old index (if not exists)
CREATE TABLE IF NOT EXISTS index_values (
    id BIGSERIAL PRIMARY KEY,
    price_date DATE UNIQUE NOT NULL,
    index_value DECIMAL(18, 6) NOT NULL,
    daily_change DECIMAL(18, 6),
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_index_price_date ON index_values(price_date);

-- +goose Down
-- migrations/000001_add_momentum_index_table.down.sql

DROP TABLE IF EXISTS momentum_index_values;
DROP INDEX IF EXISTS idx_momentum_price_date;