// settlement runs one end-of-day clearing cycle, mirroring the CBJ
// eFAWATEERcom regulatory framework: the platform does NOT move money.
// It issues a net clearing position (NCP) statement plus a separate
// commissions statement, to be submitted to the central bank's RTGS
// (which executes the actual settlement across the participants'
// settlement-bank accounts), then records the clearing postings in the
// ledger. Run it from cron at the cut-off time.
package main

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
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

const billerClearingScript = `vars {
  account $biller_payable
  account $biller_settled
  monetary $amount
}

send $amount (
  source = $biller_payable
  destination = $biller_settled
)`

const channelClearingScript = `vars {
  account $channel_due
  monetary $amount
}

send $amount (
  source = @world
  destination = $channel_due
)`

type ncpRow struct {
	participant string // biller or channel id
	role        string // "biller" | "channel"
	direction   string // "credit" (RTGS credits its settlement bank) | "debit"
	amount      *big.Int
}

func main() {
	cfg := config.Load()
	ledger := ledgerclient.New(cfg.LedgerURL, cfg.LedgerName)
	ctx := context.Background()

	// Participants: comma-separated ids, until a participant directory exists.
	billerIDs := splitList(envOr("FAWTARA_SETTLE_BILLERS", "mock-electricity,mock-water"))
	channelIDs := splitList(envOr("FAWTARA_SETTLE_CHANNELS", "bank_x"))
	asset := envOr("FAWTARA_SETTLE_ASSET", "JOD/3")
	outDir := envOr("FAWTARA_SETTLE_OUT", "settlement-out")
	batchID := "ncp_" + randomHex(8)
	cutoff := time.Now().UTC().Format("2006-01-02")

	var rows []ncpRow

	// Channel side: negative "due" balance = what the channel's settlement
	// bank owes; it appears as a debit in the NCP statement.
	for _, channelID := range channelIDs {
		due := fmt.Sprintf("channels:%s:due", channelID)
		balance, err := ledger.Balance(ctx, due, asset)
		if err != nil {
			slog.Error("balance lookup failed", "account", due, "err", err)
			continue
		}
		if balance.Sign() >= 0 {
			slog.Info("nothing due from channel", "channel", channelID)
			continue
		}
		owed := new(big.Int).Neg(balance)
		rows = append(rows, ncpRow{participant: channelID, role: "channel", direction: "debit", amount: owed})
	}

	// Biller side: payable balance = what the biller's settlement bank
	// receives; it appears as a credit in the NCP statement.
	for _, billerID := range billerIDs {
		payable := fmt.Sprintf("billers:%s:payable", billerID)
		balance, err := ledger.Balance(ctx, payable, asset)
		if err != nil {
			slog.Error("balance lookup failed", "account", payable, "err", err)
			continue
		}
		if balance.Sign() <= 0 {
			slog.Info("nothing to settle", "biller", billerID)
			continue
		}
		rows = append(rows, ncpRow{participant: billerID, role: "biller", direction: "credit", amount: balance})
	}

	if len(rows) == 0 {
		slog.Info("no positions to clear today")
		return
	}

	// 1. Issue the NCP statement (submitted to the central bank / RTGS).
	ncpPath := filepath.Join(outDir, fmt.Sprintf("ncp_payments_%s_%s.csv", cutoff, batchID))
	if err := writeNCP(ncpPath, batchID, cutoff, asset, rows); err != nil {
		slog.Error("writing NCP statement failed", "err", err)
		os.Exit(1)
	}
	slog.Info("NCP statement issued", "file", ncpPath, "batch", batchID)

	// 2. Issue the commissions statement (separate file, per the framework).
	// TODO: report per-cycle accrual (daily volumes) instead of cumulative
	// balances once the volumes endpoint is wired into the client.
	commPath := filepath.Join(outDir, fmt.Sprintf("ncp_commissions_%s_%s.csv", cutoff, batchID))
	if err := writeCommissions(ctx, ledger, commPath, batchID, cutoff, asset, channelIDs); err != nil {
		slog.Error("writing commissions statement failed", "err", err)
	} else {
		slog.Info("commissions statement issued", "file", commPath, "batch", batchID)
	}

	// 3. Record the clearing postings in the ledger, tagged with the batch id
	// so reconciliation can match them against the RTGS settlement result.
	for _, row := range rows {
		metadata := map[string]string{
			"settlement_batch_id": batchID,
			"cutoff_date":         cutoff,
			"participant":         row.participant,
			"participant_role":    row.role,
		}
		var script string
		vars := map[string]any{
			"amount": map[string]any{"asset": asset, "amount": row.amount},
		}
		if row.role == "biller" {
			script = billerClearingScript
			vars["biller_payable"] = fmt.Sprintf("billers:%s:payable", row.participant)
			vars["biller_settled"] = fmt.Sprintf("billers:%s:settled", row.participant)
		} else {
			script = channelClearingScript
			vars["channel_due"] = fmt.Sprintf("channels:%s:due", row.participant)
		}
		reference := fmt.Sprintf("clear:%s:%s:%s", row.role, row.participant, cutoff)
		if err := ledger.RunNumscript(ctx, script, vars, reference, metadata); err != nil {
			slog.Error("clearing posting failed", "participant", row.participant, "err", err)
			continue
		}
		slog.Info("cleared", "participant", row.participant, "role", row.role,
			"direction", row.direction, "amount", row.amount.String(), "batch", batchID)
	}
}

func writeNCP(path, batchID, cutoff, asset string, rows []ncpRow) error {
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
	if err := w.Write([]string{"batch_id", "cutoff_date", "participant", "role", "direction", "asset", "amount"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{batchID, cutoff, r.participant, r.role, r.direction, asset, r.amount.String()}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func writeCommissions(ctx context.Context, ledger *ledgerclient.Client, path, batchID, cutoff, asset string, channelIDs []string) error {
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
	if err := w.Write([]string{"batch_id", "cutoff_date", "beneficiary", "asset", "amount"}); err != nil {
		return err
	}
	platformFees, err := ledger.Balance(ctx, "platform:fees:revenue", asset)
	if err != nil {
		return err
	}
	if err := w.Write([]string{batchID, cutoff, "platform", asset, platformFees.String()}); err != nil {
		return err
	}
	for _, channelID := range channelIDs {
		commission, err := ledger.Balance(ctx, fmt.Sprintf("channels:%s:commission", channelID), asset)
		if err != nil {
			return err
		}
		if err := w.Write([]string{batchID, cutoff, "channel:" + channelID, asset, commission.String()}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
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

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
