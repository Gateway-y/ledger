// recon publishes the daily reconciliation files required by the CBJ
// framework: after end of business day, one file per participant (channel or
// biller) listing the day's executed payment movements, for the participant
// to match against the notices it stored during the day. Mismatches are
// investigated and, when a movement was recorded in error, reversed via
// POST /v1/payments/{id}/reverse on channel-api.
package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gateway-y/fawtara-platform/internal/config"
	"github.com/gateway-y/fawtara-platform/internal/ledgerclient"
)

func main() {
	cfg := config.Load()
	ledger := ledgerclient.New(cfg.LedgerURL, cfg.LedgerName)
	ctx := context.Background()

	day := envOr("FAWTARA_RECON_DATE", time.Now().UTC().Format("2006-01-02"))
	outDir := envOr("FAWTARA_RECON_OUT", "recon-out")
	channels := splitList(envOr("FAWTARA_RECON_CHANNELS", "bank_x"))
	billerIDs := splitList(envOr("FAWTARA_RECON_BILLERS", "mock-electricity,mock-water"))

	dayStart, err := time.Parse("2006-01-02", day)
	if err != nil {
		slog.Error("invalid FAWTARA_RECON_DATE (want YYYY-MM-DD)", "value", day)
		os.Exit(1)
	}
	dayEnd := dayStart.Add(24 * time.Hour)

	failed := false
	for _, channelID := range channels {
		path := filepath.Join(outDir, fmt.Sprintf("recon_channel_%s_%s.csv", channelID, day))
		if err := writeParticipantFile(ctx, ledger, path, "channel_id", channelID, dayStart, dayEnd); err != nil {
			slog.Error("reconciliation file failed", "participant", channelID, "err", err)
			failed = true
		}
	}
	for _, billerID := range billerIDs {
		path := filepath.Join(outDir, fmt.Sprintf("recon_biller_%s_%s.csv", billerID, day))
		if err := writeParticipantFile(ctx, ledger, path, "biller_id", billerID, dayStart, dayEnd); err != nil {
			slog.Error("reconciliation file failed", "participant", billerID, "err", err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func writeParticipantFile(ctx context.Context, ledger *ledgerclient.Client, path, metadataKey, participant string, dayStart, dayEnd time.Time) error {
	txs, err := ledger.ListTransactions(ctx, map[string]any{
		"$and": []any{
			map[string]any{"$match": map[string]any{"metadata[" + metadataKey + "]": participant}},
			map[string]any{"$gte": map[string]any{"timestamp": dayStart.Format(time.RFC3339)}},
			map[string]any{"$lt": map[string]any{"timestamp": dayEnd.Format(time.RFC3339)}},
		},
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"payment_id", "bill_ref", "subscriber_ref", "reference",
		"timestamp", "asset", "gross_amount", "net_to_biller", "reverted"}); err != nil {
		return err
	}
	for _, tx := range txs {
		gross, netToBiller, asset := amounts(tx)
		if err := w.Write([]string{
			tx.Metadata["payment_id"],
			tx.Metadata["bill_ref"],
			tx.Metadata["subscriber_ref"],
			tx.Reference,
			tx.Timestamp.UTC().Format(time.RFC3339),
			asset,
			gross.String(),
			netToBiller.String(),
			fmt.Sprintf("%t", tx.Reverted),
		}); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	slog.Info("reconciliation file issued", "file", path, "movements", len(txs))
	return nil
}

// amounts derives the gross bill amount (everything sourced from the
// channel's due account) and the biller's net share from a payment
// transaction's postings.
func amounts(tx ledgerclient.Transaction) (gross, netToBiller *big.Int, asset string) {
	gross, netToBiller = big.NewInt(0), big.NewInt(0)
	for _, p := range tx.Postings {
		if p.Amount == nil {
			continue
		}
		asset = p.Asset
		if strings.HasPrefix(p.Source, "channels:") && strings.HasSuffix(p.Source, ":due") {
			gross.Add(gross, p.Amount)
		}
		if strings.HasPrefix(p.Destination, "billers:") && strings.HasSuffix(p.Destination, ":payable") {
			netToBiller.Add(netToBiller, p.Amount)
		}
	}
	return gross, netToBiller, asset
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
