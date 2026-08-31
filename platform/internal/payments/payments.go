// Package payments implements the core bill-payment flow: write the ledger
// transaction, then notify the biller.
package payments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/gateway-y/fawtara-platform/internal/billers"
	"github.com/gateway-y/fawtara-platform/internal/ledgerclient"
)

// billPaymentScript mirrors numscripts/bill_payment.num (kept inline so the
// binary is self-contained; the .num files remain the reviewed reference).
const billPaymentScript = `vars {
  monetary $amount
  account $channel_due
  account $biller_payable
  account $channel_commission
  portion $fee_share
  portion $commission_share
}

send $amount (
  source = $channel_due allowing unbounded overdraft
  destination = {
    $fee_share to @platform:fees:revenue
    $commission_share to $channel_commission
    remaining to $biller_payable
  }
)`

type Service struct {
	Ledger  *ledgerclient.Client
	Billers *billers.Registry

	// FeeShare / CommissionShare are numscript portions, e.g. "2%" and "1%".
	FeeShare        string
	CommissionShare string
}

type Request struct {
	BillerID       string `json:"biller_id"`
	SubscriberRef  string `json:"subscriber_ref"`
	BillRef        string `json:"bill_ref"`
	ChannelID      string `json:"channel_id"`
	Amount         int64  `json:"amount"`
	Asset          string `json:"asset"`
	IdempotencyKey string `json:"idempotency_key"`
}

type Result struct {
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"`
}

func (s *Service) Pay(ctx context.Context, req Request) (Result, error) {
	if req.Amount <= 0 {
		return Result{}, fmt.Errorf("amount must be positive")
	}
	if req.Asset == "" {
		req.Asset = "JOD/3"
	}
	if _, err := s.Billers.Get(req.BillerID); err != nil {
		return Result{}, err
	}

	paymentID := newID()
	reference := req.IdempotencyKey
	if reference == "" {
		reference = paymentID
	}

	vars := map[string]any{
		"amount":             map[string]any{"asset": req.Asset, "amount": req.Amount},
		"channel_due":        fmt.Sprintf("channels:%s:due", req.ChannelID),
		"biller_payable":     fmt.Sprintf("billers:%s:payable", req.BillerID),
		"channel_commission": fmt.Sprintf("channels:%s:commission", req.ChannelID),
		"fee_share":          s.FeeShare,
		"commission_share":   s.CommissionShare,
	}
	metadata := map[string]string{
		"payment_id":     paymentID,
		"biller_id":      req.BillerID,
		"channel_id":     req.ChannelID,
		"subscriber_ref": req.SubscriberRef,
		"bill_ref":       req.BillRef,
	}
	if err := s.Ledger.RunNumscript(ctx, billPaymentScript, vars, reference, metadata); err != nil {
		return Result{}, fmt.Errorf("ledger posting failed: %w", err)
	}

	adapter, _ := s.Billers.Get(req.BillerID)
	if err := adapter.NotifyPayment(ctx, req.BillRef, paymentID, req.Amount); err != nil {
		// The money is recorded; biller notification is retried out-of-band.
		return Result{PaymentID: paymentID, Status: "paid_notification_pending"}, nil
	}
	return Result{PaymentID: paymentID, Status: "paid"}, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "pay_" + hex.EncodeToString(b)
}
