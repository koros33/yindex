package models

import "time"

// IndexValue represents a single day's index value
type IndexValue struct {
	Date        time.Time `json:"date"`
	IndexValue  float64   `json:"index_value"`
	DailyChange float64   `json:"daily_change"`
}

// IndexLatest is what /api/index/latest returns
type IndexLatest struct {
	Date        time.Time `json:"date"`
	IndexValue  float64   `json:"index_value"`
	DailyChange float64   `json:"daily_change"`
	DailyPct    float64   `json:"daily_change_pct"`
}

// IndexStats is what /api/index/stats returns
type IndexStats struct {
	Current    float64   `json:"current"`
	AllTimeHigh float64  `json:"all_time_high"`
	AllTimeLow  float64  `json:"all_time_low"`
	YTDChange  float64   `json:"ytd_change"`
	YTDPct     float64   `json:"ytd_change_pct"`
	OneMonthPct float64  `json:"one_month_pct"`
	BaseDate   time.Time `json:"base_date"`
	BaseValue  float64   `json:"base_value"`
}