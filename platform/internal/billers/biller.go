// Package billers defines the adapter interface every biller integration
// implements, plus a registry and a mock adapter for development.
package billers

import (
	"context"
	"fmt"
	"sync"
)

// Bill is a single outstanding bill as presented by a biller.
type Bill struct {
	BillerID      string `json:"biller_id"`
	SubscriberRef string `json:"subscriber_ref"`
	BillRef       string `json:"bill_ref"`
	Amount        int64  `json:"amount"` // minor units (e.g. JOD millimes)
	Asset         string `json:"asset"`
	DueDate       string `json:"due_date"`
	Description   string `json:"description"`
}

// Adapter is implemented once per biller (REST, SOAP, ISO 8583, file-based...).
type Adapter interface {
	// Inquire returns the outstanding bills for a subscriber.
	Inquire(ctx context.Context, subscriberRef string) ([]Bill, error)
	// NotifyPayment informs the biller that a bill was paid.
	NotifyPayment(ctx context.Context, billRef, paymentID string, amount int64) error
}

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}}
}

func (r *Registry) Register(billerID string, a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[billerID] = a
}

func (r *Registry) Get(billerID string) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[billerID]
	if !ok {
		return nil, fmt.Errorf("unknown biller %q", billerID)
	}
	return a, nil
}
