// Package store persists the platform's own regulatory data in Postgres:
// customer profiles (nationality + identity document, per the CBJ framework),
// their bill subscriptions, and short-lived inquiry tickets that enforce
// inquire-before-pay.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrDuplicate       = errors.New("already exists")
	ErrInquiryMismatch = errors.New("no valid inquiry for this payment (inquire first)")
)

type Customer struct {
	ID          string    `json:"customer_id"`
	Nationality string    `json:"nationality"`
	IDDocType   string    `json:"id_doc_type"`
	IDDocNumber string    `json:"id_doc_number"`
	CreatedAt   time.Time `json:"created_at"`
}

type Subscription struct {
	ID            string    `json:"subscription_id"`
	CustomerID    string    `json:"customer_id"`
	BillerID      string    `json:"biller_id"`
	SubscriberRef string    `json:"subscriber_ref"`
	Label         string    `json:"label"`
	CreatedAt     time.Time `json:"created_at"`
}

type Inquiry struct {
	ID            string
	BillerID      string
	SubscriberRef string
	BillRef       string
	Amount        int64
	Asset         string
	ExpiresAt     time.Time
}

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, uri string) (*Store, error) {
	db, err := sql.Open("postgres", uri)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("postgres unreachable: %w", err)
	}
	s := &Store{db: db}
	return s, s.migrate(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS fawtara_customers (
    id            text PRIMARY KEY,
    nationality   text NOT NULL,
    id_doc_type   text NOT NULL,
    id_doc_number text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (nationality, id_doc_type, id_doc_number)
);
CREATE TABLE IF NOT EXISTS fawtara_subscriptions (
    id             text PRIMARY KEY,
    customer_id    text NOT NULL REFERENCES fawtara_customers(id),
    biller_id      text NOT NULL,
    subscriber_ref text NOT NULL,
    label          text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,
    UNIQUE (customer_id, biller_id, subscriber_ref)
);
CREATE TABLE IF NOT EXISTS fawtara_inquiries (
    id             text PRIMARY KEY,
    biller_id      text NOT NULL,
    subscriber_ref text NOT NULL,
    bill_ref       text NOT NULL,
    amount         bigint NOT NULL,
    asset          text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    used_at        timestamptz
);`)
	return err
}

// --- Customer profiles -----------------------------------------------------

func (s *Store) CreateCustomer(ctx context.Context, nationality, docType, docNumber string) (Customer, error) {
	c := Customer{
		ID:          "cust_" + randomHex(8),
		Nationality: nationality,
		IDDocType:   docType,
		IDDocNumber: docNumber,
	}
	err := s.db.QueryRowContext(ctx, `
INSERT INTO fawtara_customers (id, nationality, id_doc_type, id_doc_number)
VALUES ($1, $2, $3, $4)
ON CONFLICT (nationality, id_doc_type, id_doc_number) DO NOTHING
RETURNING created_at`,
		c.ID, nationality, docType, docNumber).Scan(&c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Conflict: the customer is already registered — return the existing
		// profile so registration stays idempotent for banks/PSPs.
		return s.FindCustomerByDocument(ctx, nationality, docType, docNumber)
	}
	return c, err
}

func (s *Store) GetCustomer(ctx context.Context, id string) (Customer, error) {
	var c Customer
	err := s.db.QueryRowContext(ctx, `
SELECT id, nationality, id_doc_type, id_doc_number, created_at
FROM fawtara_customers WHERE id = $1`, id).
		Scan(&c.ID, &c.Nationality, &c.IDDocType, &c.IDDocNumber, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) FindCustomerByDocument(ctx context.Context, nationality, docType, docNumber string) (Customer, error) {
	var c Customer
	err := s.db.QueryRowContext(ctx, `
SELECT id, nationality, id_doc_type, id_doc_number, created_at
FROM fawtara_customers
WHERE nationality = $1 AND id_doc_type = $2 AND id_doc_number = $3`,
		nationality, docType, docNumber).
		Scan(&c.ID, &c.Nationality, &c.IDDocType, &c.IDDocNumber, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// --- Subscriptions ---------------------------------------------------------

func (s *Store) AddSubscription(ctx context.Context, customerID, billerID, subscriberRef, label string) (Subscription, error) {
	sub := Subscription{
		ID:            "sub_" + randomHex(8),
		CustomerID:    customerID,
		BillerID:      billerID,
		SubscriberRef: subscriberRef,
		Label:         label,
	}
	err := s.db.QueryRowContext(ctx, `
INSERT INTO fawtara_subscriptions (id, customer_id, biller_id, subscriber_ref, label)
VALUES ($1, $2, $3, $4, $5)
RETURNING created_at`,
		sub.ID, customerID, billerID, subscriberRef, label).Scan(&sub.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return sub, ErrDuplicate
	}
	return sub, err
}

func (s *Store) UpdateSubscription(ctx context.Context, customerID, subscriptionID, label string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE fawtara_subscriptions SET label = $1
WHERE id = $2 AND customer_id = $3 AND deleted_at IS NULL`,
		label, subscriptionID, customerID)
	if err != nil {
		return err
	}
	return checkAffected(res)
}

func (s *Store) DeleteSubscription(ctx context.Context, customerID, subscriptionID string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE fawtara_subscriptions SET deleted_at = now()
WHERE id = $1 AND customer_id = $2 AND deleted_at IS NULL`,
		subscriptionID, customerID)
	if err != nil {
		return err
	}
	return checkAffected(res)
}

func (s *Store) ListSubscriptions(ctx context.Context, customerID string) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, customer_id, biller_id, subscriber_ref, label, created_at
FROM fawtara_subscriptions
WHERE customer_id = $1 AND deleted_at IS NULL
ORDER BY created_at`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	subs := []Subscription{}
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.CustomerID, &sub.BillerID, &sub.SubscriberRef, &sub.Label, &sub.CreatedAt); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// --- Inquiry tickets (inquire-before-pay enforcement) ----------------------

func (s *Store) SaveInquiry(ctx context.Context, inq Inquiry) (string, error) {
	id := "inq_" + randomHex(8)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO fawtara_inquiries (id, biller_id, subscriber_ref, bill_ref, amount, asset, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, inq.BillerID, inq.SubscriberRef, inq.BillRef, inq.Amount, inq.Asset, inq.ExpiresAt)
	return id, err
}

// ConsumeInquiry atomically validates and single-uses an inquiry ticket: it
// must exist, match the payment's biller/subscriber/amount, be unexpired and
// unused. This is what makes inquiry-before-payment mandatory.
func (s *Store) ConsumeInquiry(ctx context.Context, inquiryID, billerID, subscriberRef string, amount int64) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE fawtara_inquiries SET used_at = now()
WHERE id = $1 AND biller_id = $2 AND subscriber_ref = $3 AND amount = $4
  AND used_at IS NULL AND expires_at > now()`,
		inquiryID, billerID, subscriberRef, amount)
	if err != nil {
		return err
	}
	if err := checkAffected(res); errors.Is(err, ErrNotFound) {
		return ErrInquiryMismatch
	} else if err != nil {
		return err
	}
	return nil
}

func checkAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505" // unique_violation
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
