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

	// Per the CBJ framework, the system publishes daily reconciliation files
	// of executed payments; each participant matches its own payment notices
	// against them, and mismatches are investigated and reversed if needed.
	// This scaffold reads our ledger side; next steps are exporting the daily
	// reconciliation file per participant and matching the RTGS settlement
	// result (by settlement_batch_id) against the issued NCP statements.
	channels := strings.Split(envOr("FAWTARA_RECON_CHANNELS", "bank_x"), ",")
	for _, ch := range channels {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			continue
		}
		account := fmt.Sprintf("channels:%s:due", ch)
		balance, err := ledger.Balance(ctx, account, asset)
		if err != nil {
			slog.Error("ledger balance failed", "account", account, "err", err)
			continue
		}
		slog.Info("ledger side", "account", account, "asset", asset, "balance", balance.String())
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
