package handlers

import (
	"encoding/json"
	"net/http"
	"time"
)

// writeJSON is shared by all handlers
func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func HealthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC(),
	})
}