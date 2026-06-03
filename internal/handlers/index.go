package handlers

import (
	"net/http"

	"github.com/koros33/yindex/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IndexHandler struct {
	DB *pgxpool.Pool
}

func (h *IndexHandler) Latest(w http.ResponseWriter, r *http.Request) {
	data, err := db.GetIndexLatest(r.Context(), h.DB)
	if err != nil {
		http.Error(w, "failed to fetch latest index", http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}

func (h *IndexHandler) History(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "ytd"
	}
	data, err := db.GetIndexHistory(r.Context(), h.DB, period)
	if err != nil {
		http.Error(w, "invalid period or DB error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, data)
}

func (h *IndexHandler) Stats(w http.ResponseWriter, r *http.Request) {
	data, err := db.GetIndexStats(r.Context(), h.DB)
	if err != nil {
		http.Error(w, "failed to fetch index stats", http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}