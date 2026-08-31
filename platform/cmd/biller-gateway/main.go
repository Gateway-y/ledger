// biller-gateway hosts the per-biller adapters behind an internal HTTP API.
// channel-api can either embed the registry directly (current setup) or call
// this service once adapters need isolation and independent scaling.
package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"github.com/gateway-y/fawtara-platform/internal/billers"
)

func main() {
	registry := billers.NewRegistry()
	registry.Register("mock-electricity", billers.Mock{BillerID: "mock-electricity"})
	registry.Register("mock-water", billers.Mock{BillerID: "mock-water"})

	mux := http.NewServeMux()

	mux.HandleFunc("POST /internal/v1/inquire", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			BillerID      string `json:"biller_id"`
			SubscriberRef string `json:"subscriber_ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		adapter, err := registry.Get(req.BillerID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		bills, err := adapter.Inquire(r.Context(), req.SubscriberRef)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"bills": bills})
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := envOr("FAWTARA_BILLER_GATEWAY_ADDR", ":8082")
	slog.Info("biller-gateway listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
