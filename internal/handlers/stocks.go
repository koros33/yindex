package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/koros33/yindex/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type StockHandler struct {
	DB *pgxpool.Pool
}

func (h *StockHandler) All(w http.ResponseWriter, r *http.Request) {
	data, err := db.GetAllStocks(r.Context(), h.DB)
	if err != nil {
		http.Error(w, "failed to fetch stocks", http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}

func (h *StockHandler) ByTicker(w http.ResponseWriter, r *http.Request) {
	ticker := strings.ToUpper(chi.URLParam(r, "ticker"))
	data, err := db.GetStockByTicker(r.Context(), h.DB, ticker)
	if err != nil {
		http.Error(w, "stock not found", http.StatusNotFound)
		return
	}
	writeJSON(w, data)
}

func (h *StockHandler) History(w http.ResponseWriter, r *http.Request) {
	ticker := strings.ToUpper(chi.URLParam(r, "ticker"))
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "1m"
	}
	data, err := db.GetStockHistory(r.Context(), h.DB, ticker, period)
	if err != nil {
		http.Error(w, "failed to fetch stock history: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, data)
}

func (h *StockHandler) Stats(w http.ResponseWriter, r *http.Request) {
	ticker := strings.ToUpper(chi.URLParam(r, "ticker"))
	data, err := db.GetStockStats(r.Context(), h.DB, ticker)
	if err != nil {
		http.Error(w, "failed to fetch stock stats", http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}