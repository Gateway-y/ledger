// channel-api is the public API consumed by payment channels (banks, wallets,
// agent apps): bill inquiry and bill payment.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"github.com/gateway-y/fawtara-platform/internal/billers"
	"github.com/gateway-y/fawtara-platform/internal/config"
	"github.com/gateway-y/fawtara-platform/internal/ledgerclient"
	"github.com/gateway-y/fawtara-platform/internal/payments"
)

func main() {
	cfg := config.Load()
	ledger := ledgerclient.New(cfg.LedgerURL, cfg.LedgerName)
	if err := ledger.EnsureLedger(context.Background()); err != nil {
		slog.Warn("could not ensure ledger (is it running?)", "err", err)
	}

	registry := billers.NewRegistry()
	registry.Register("mock-electricity", billers.Mock{BillerID: "mock-electricity"})
	registry.Register("mock-water", billers.Mock{BillerID: "mock-water"})

	svc := &payments.Service{
		Ledger:          ledger,
		Billers:         registry,
		FeeShare:        "2%",
		CommissionShare: "1%",
	}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/bills/inquiry", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			BillerID      string `json:"biller_id"`
			SubscriberRef string `json:"subscriber_ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		adapter, err := registry.Get(req.BillerID)
		if err != nil {
			httpError(w, http.StatusNotFound, err)
			return
		}
		bills, err := adapter.Inquire(r.Context(), req.SubscriberRef)
		if err != nil {
			httpError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"bills": bills})
	})

	mux.HandleFunc("POST /v1/payments", func(w http.ResponseWriter, r *http.Request) {
		var req payments.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		res, err := svc.Pay(r.Context(), req)
		if err != nil {
			httpError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := envOr("FAWTARA_CHANNEL_API_ADDR", ":8081")
	slog.Info("channel-api listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
