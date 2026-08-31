// channel-api is the public API consumed by payment channels (banks, wallets,
// agent apps): customer profile registration, bill subscriptions, bill
// inquiry, bill payment (inquiry-first enforced), status and reversal.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gateway-y/fawtara-platform/internal/billers"
	"github.com/gateway-y/fawtara-platform/internal/config"
	"github.com/gateway-y/fawtara-platform/internal/ledgerclient"
	"github.com/gateway-y/fawtara-platform/internal/payments"
	"github.com/gateway-y/fawtara-platform/internal/store"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	ledger := ledgerclient.New(cfg.LedgerURL, cfg.LedgerName)
	if err := ledger.EnsureLedger(ctx); err != nil {
		slog.Warn("could not ensure ledger (is it running?)", "err", err)
	}

	db, err := store.Open(ctx, envOr("FAWTARA_DB_URI",
		"postgres://ledger:ledger@localhost:5432/ledger?sslmode=disable"))
	if err != nil {
		slog.Error("opening platform store failed", "err", err)
		os.Exit(1)
	}

	inquiryTTL := 30 * time.Minute
	if v, err := strconv.Atoi(envOr("FAWTARA_INQUIRY_TTL_MINUTES", "30")); err == nil && v > 0 {
		inquiryTTL = time.Duration(v) * time.Minute
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

	// --- Customer profiles (CBJ framework: nationality + identity document) --

	mux.HandleFunc("POST /v1/customers", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Nationality string `json:"nationality"`
			IDDocType   string `json:"id_doc_type"`
			IDDocNumber string `json:"id_doc_number"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		if req.Nationality == "" || req.IDDocType == "" || req.IDDocNumber == "" {
			httpError(w, http.StatusBadRequest, errors.New("nationality, id_doc_type and id_doc_number are required"))
			return
		}
		customer, err := db.CreateCustomer(r.Context(), req.Nationality, req.IDDocType, req.IDDocNumber)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, customer)
	})

	mux.HandleFunc("GET /v1/customers/{id}", func(w http.ResponseWriter, r *http.Request) {
		customer, err := db.GetCustomer(r.Context(), r.PathValue("id"))
		if err != nil {
			httpError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, customer)
	})

	// Lookup by identity document, per the framework's inquiry requirement.
	mux.HandleFunc("GET /v1/customers", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		customer, err := db.FindCustomerByDocument(r.Context(),
			q.Get("nationality"), q.Get("id_doc_type"), q.Get("id_doc_number"))
		if err != nil {
			httpError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, customer)
	})

	// --- Subscriptions (add / update / delete / inquire) ---------------------

	mux.HandleFunc("POST /v1/customers/{id}/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			BillerID      string `json:"biller_id"`
			SubscriberRef string `json:"subscriber_ref"`
			Label         string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		if _, err := registry.Get(req.BillerID); err != nil {
			httpError(w, http.StatusNotFound, err)
			return
		}
		if _, err := db.GetCustomer(r.Context(), r.PathValue("id")); err != nil {
			httpError(w, statusFor(err), err)
			return
		}
		sub, err := db.AddSubscription(r.Context(), r.PathValue("id"), req.BillerID, req.SubscriberRef, req.Label)
		if err != nil {
			httpError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, sub)
	})

	mux.HandleFunc("GET /v1/customers/{id}/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		subs, err := db.ListSubscriptions(r.Context(), r.PathValue("id"))
		if err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
	})

	mux.HandleFunc("PUT /v1/customers/{id}/subscriptions/{subID}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		if err := db.UpdateSubscription(r.Context(), r.PathValue("id"), r.PathValue("subID"), req.Label); err != nil {
			httpError(w, statusFor(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /v1/customers/{id}/subscriptions/{subID}", func(w http.ResponseWriter, r *http.Request) {
		if err := db.DeleteSubscription(r.Context(), r.PathValue("id"), r.PathValue("subID")); err != nil {
			httpError(w, statusFor(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// --- Bills: inquiry and payment ------------------------------------------

	// Each returned bill carries an inquiry_id ticket; payments are accepted
	// only against a valid, unexpired, unused ticket (inquire-before-pay).
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
		type presentedBill struct {
			billers.Bill
			InquiryID string `json:"inquiry_id"`
			ExpiresAt string `json:"inquiry_expires_at"`
		}
		out := make([]presentedBill, 0, len(bills))
		for _, bill := range bills {
			expiresAt := time.Now().Add(inquiryTTL)
			inquiryID, err := db.SaveInquiry(r.Context(), store.Inquiry{
				BillerID:      bill.BillerID,
				SubscriberRef: bill.SubscriberRef,
				BillRef:       bill.BillRef,
				Amount:        bill.Amount,
				Asset:         bill.Asset,
				ExpiresAt:     expiresAt,
			})
			if err != nil {
				httpError(w, http.StatusInternalServerError, err)
				return
			}
			out = append(out, presentedBill{Bill: bill, InquiryID: inquiryID, ExpiresAt: expiresAt.UTC().Format(time.RFC3339)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"bills": out})
	})

	mux.HandleFunc("POST /v1/payments", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			payments.Request
			InquiryID string `json:"inquiry_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		if req.InquiryID == "" {
			httpError(w, http.StatusUnprocessableEntity, store.ErrInquiryMismatch)
			return
		}
		if err := db.ConsumeInquiry(r.Context(), req.InquiryID, req.BillerID, req.SubscriberRef, req.Amount); err != nil {
			httpError(w, statusFor(err), err)
			return
		}
		res, err := svc.Pay(r.Context(), req.Request)
		if err != nil {
			httpError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	})

	mux.HandleFunc("GET /v1/payments/{id}", func(w http.ResponseWriter, r *http.Request) {
		tx, err := ledger.FindByMetadata(r.Context(), "payment_id", r.PathValue("id"))
		if err != nil {
			httpError(w, http.StatusNotFound, err)
			return
		}
		status := "paid"
		if tx.Reverted {
			status = "reversed"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"payment_id": r.PathValue("id"),
			"status":     status,
			"timestamp":  tx.Timestamp,
			"metadata":   tx.Metadata,
		})
	})

	// Reversal of an erroneous payment (CBJ framework: mismatched entries must
	// be reversed). Uses the ledger's native revert, so the audit trail keeps
	// both the original and the reversing transaction.
	mux.HandleFunc("POST /v1/payments/{id}/reverse", func(w http.ResponseWriter, r *http.Request) {
		tx, err := ledger.FindByMetadata(r.Context(), "payment_id", r.PathValue("id"))
		if err != nil {
			httpError(w, http.StatusNotFound, err)
			return
		}
		if tx.Reverted {
			httpError(w, http.StatusConflict, errors.New("payment already reversed"))
			return
		}
		if err := ledger.Revert(r.Context(), tx.ID); err != nil {
			httpError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"payment_id": r.PathValue("id"),
			"status":     "reversed",
		})
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

func statusFor(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrDuplicate):
		return http.StatusConflict
	case errors.Is(err, store.ErrInquiryMismatch):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
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
