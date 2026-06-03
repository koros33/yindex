package models

import "time"

// Stock represents a constituent of the index
type Stock struct {
	ID     int    `json:"id"`
	Ticker string `json:"ticker"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// StockPrice is a single day close price
type StockPrice struct {
	Date       time.Time `json:"date"`
	ClosePrice float64   `json:"close_price"`
}

// StockStats is what /api/stocks/:ticker/stats returns
type StockStats struct {
	Ticker      string    `json:"ticker"`
	Name        string    `json:"name"`
	Latest      float64   `json:"latest_price"`
	DailyPct    float64   `json:"daily_change_pct"`
	OneMonthPct float64   `json:"one_month_pct"`
	YTDPct      float64   `json:"ytd_change_pct"`
	AllTimeHigh float64   `json:"all_time_high"`
	AllTimeLow  float64   `json:"all_time_low"`
	LastUpdated time.Time `json:"last_updated"`
}