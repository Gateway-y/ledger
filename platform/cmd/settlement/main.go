// settlement runs one settlement cycle: for each biller, read the payable
// balance at cut-off, post the settlement transaction, and emit a payout
// instruction. Run it from cron (daily at cut-off time).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gateway-y/fawtara-platform/internal/config"
	"github.com/gateway-y/fawtara-platform/internal/ledgerclient"
)

const settlementScript = `vars {
  account $biller_payable
  account $biller_settled
  monetary $amount
}

send $amount (
  source = $biller_payable
  destination = $biller_settled
)`

func main() {
	cfg := config.Load()
	ledger := ledgerclient.New(cfg.LedgerURL, cfg.LedgerName)
	ctx := context.Background()

	// Billers to settle: comma-separated ids, until a biller directory exists.
	billerIDs := strings.Split(envOr("FAWTARA_SETTLE_BILLERS", "mock-electricity,mock-water"), ",")
	asset := envOr("FAWTARA_SETTLE_ASSET", "JOD/3")
	batchID := "batch_" + randomHex(8)
	cutoff := time.Now().UTC().Format("2006-01-02")

	for _, billerID := range billerIDs {
		billerID = strings.TrimSpace(billerID)
		if billerID == "" {
			continue
		}
		payable := fmt.Sprintf("billers:%s:payable", billerID)
		balance, err := ledger.Balance(ctx, payable, asset)
		if err != nil {
			slog.Error("balance lookup failed", "biller", billerID, "err", err)
			continue
		}
		if balance.Sign() <= 0 {
			slog.Info("nothing to settle", "biller", billerID)
			continue
		}
		vars := map[string]any{
			"biller_payable": payable,
			"biller_settled": fmt.Sprintf("billers:%s:settled", billerID),
			"amount":         map[string]any{"asset": asset, "amount": balance},
		}
		metadata := map[string]string{
			"settlement_batch_id": batchID,
			"cutoff_date":         cutoff,
			"biller_id":           billerID,
		}
		if err := ledger.RunNumscript(ctx, settlementScript, vars,
			fmt.Sprintf("settle:%s:%s", billerID, cutoff), metadata); err != nil {
			slog.Error("settlement posting failed", "biller", billerID, "err", err)
			continue
		}
		// TODO: emit the actual payout instruction (ACH file / CliQ transfer)
		// carrying the same batchID, toward the payout rail.
		slog.Info("settled", "biller", billerID, "amount", balance.String(), "batch", batchID)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
