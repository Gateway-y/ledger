// recon compares ledger balances against external sources (bank statements,
// biller confirmation files) and reports drift. This scaffold reads the
// ledger side; external-source loaders plug in per institution.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/gateway-y/fawtara-platform/internal/config"
	"github.com/gateway-y/fawtara-platform/internal/ledgerclient"
)

func main() {
	cfg := config.Load()
	ledger := ledgerclient.New(cfg.LedgerURL, cfg.LedgerName)
	ctx := context.Background()
	asset := envOr("FAWTARA_RECON_ASSET", "JOD/3")

	// Channels whose collections accounts must match bank statement credits.
	channels := strings.Split(envOr("FAWTARA_RECON_CHANNELS", "bank_x"), ",")
	for _, ch := range channels {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			continue
		}
		account := fmt.Sprintf("channels:%s:collections", ch)
		balance, err := ledger.Balance(ctx, account, asset)
		if err != nil {
			slog.Error("ledger balance failed", "account", account, "err", err)
			continue
		}
		// TODO: load the bank statement balance for this channel and compute
		// drift = statement - ledger; flag any non-zero drift.
		slog.Info("ledger side", "account", account, "asset", asset, "balance", balance.String())
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
