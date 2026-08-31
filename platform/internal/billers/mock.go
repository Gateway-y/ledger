package billers

import (
	"context"
	"fmt"
	"log/slog"
)

// Mock is a development adapter that always presents one bill per subscriber.
type Mock struct {
	BillerID string
}

func (m Mock) Inquire(_ context.Context, subscriberRef string) ([]Bill, error) {
	return []Bill{{
		BillerID:      m.BillerID,
		SubscriberRef: subscriberRef,
		BillRef:       fmt.Sprintf("%s-%s-2026-08", m.BillerID, subscriberRef),
		Amount:        45500,
		Asset:         "JOD/3",
		DueDate:       "2026-09-15",
		Description:   "Monthly bill (mock)",
	}}, nil
}

func (m Mock) NotifyPayment(_ context.Context, billRef, paymentID string, amount int64) error {
	slog.Info("mock biller notified", "biller", m.BillerID, "bill_ref", billRef,
		"payment_id", paymentID, "amount", amount)
	return nil
}
