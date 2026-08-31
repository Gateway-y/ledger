// Package ledgerclient is a thin HTTP client for the Formance Ledger v2 API,
// covering only what the platform needs: creating numscript transactions and
// reading account balances.
package ledgerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	baseURL string
	ledger  string
	http    *http.Client
}

func New(baseURL, ledger string) *Client {
	return &Client{
		baseURL: baseURL,
		ledger:  ledger,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// EnsureLedger creates the ledger if it does not exist yet (idempotent).
func (c *Client) EnsureLedger(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/v2/%s", c.baseURL, url.PathEscape(c.ledger)), bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 201 = created, 400 with LEDGER_ALREADY_EXISTS is fine too.
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ensure ledger: status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// RunNumscript posts a numscript transaction. vars values follow the ledger API
// conventions (accounts as strings, monetary as {asset, amount}).
// The idempotency key is passed as the transaction reference so replays of the
// same payment are rejected by the ledger.
func (c *Client) RunNumscript(ctx context.Context, script string, vars map[string]any, reference string, metadata map[string]string) error {
	payload := map[string]any{
		"script": map[string]any{
			"plain": script,
			"vars":  vars,
		},
		"metadata": metadata,
	}
	if reference != "" {
		payload["reference"] = reference
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/v2/%s/transactions", c.baseURL, url.PathEscape(c.ledger)), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create transaction: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// Posting is one ledger movement inside a transaction.
type Posting struct {
	Source      string   `json:"source"`
	Destination string   `json:"destination"`
	Amount      *big.Int `json:"amount"`
	Asset       string   `json:"asset"`
}

// Transaction is the subset of the ledger transaction model the platform uses.
type Transaction struct {
	ID        *big.Int          `json:"id"`
	Reference string            `json:"reference"`
	Timestamp time.Time         `json:"timestamp"`
	Reverted  bool              `json:"reverted"`
	Metadata  map[string]string `json:"metadata"`
	Postings  []Posting         `json:"postings"`
}

// ListTransactions returns all transactions matching the given filter (the
// ledger's query DSL: {"$match": ...}, {"$and": [...]}, {"$gte": ...}),
// following pagination cursors until exhaustion.
func (c *Client) ListTransactions(ctx context.Context, filter map[string]any) ([]Transaction, error) {
	var out []Transaction
	cursor := ""
	for {
		u := fmt.Sprintf("%s/v2/%s/transactions?pageSize=100", c.baseURL, url.PathEscape(c.ledger))
		var reqBody io.Reader
		if cursor != "" {
			u = fmt.Sprintf("%s/v2/%s/transactions?cursor=%s", c.baseURL, url.PathEscape(c.ledger), url.QueryEscape(cursor))
		} else if filter != nil {
			body, err := json.Marshal(filter)
			if err != nil {
				return nil, err
			}
			reqBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, reqBody)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		var page struct {
			Cursor struct {
				HasMore bool          `json:"hasMore"`
				Next    string        `json:"next"`
				Data    []Transaction `json:"data"`
			} `json:"cursor"`
		}
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("list transactions: status %d: %s", resp.StatusCode, respBody)
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, page.Cursor.Data...)
		if !page.Cursor.HasMore || page.Cursor.Next == "" {
			return out, nil
		}
		cursor = page.Cursor.Next
	}
}

// FindByMetadata returns the first transaction carrying the given metadata
// key/value, or an error when none exists.
func (c *Client) FindByMetadata(ctx context.Context, key, value string) (*Transaction, error) {
	txs, err := c.ListTransactions(ctx, map[string]any{
		"$match": map[string]any{fmt.Sprintf("metadata[%s]", key): value},
	})
	if err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return nil, fmt.Errorf("no transaction with %s=%s", key, value)
	}
	return &txs[0], nil
}

// Revert posts the ledger-native revert of a transaction: a new transaction
// with the exact opposite postings, leaving the audit trail intact.
func (c *Client) Revert(ctx context.Context, id *big.Int) error {
	u := fmt.Sprintf("%s/v2/%s/transactions/%s/revert", c.baseURL, url.PathEscape(c.ledger), id.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("revert transaction %s: status %d: %s", id, resp.StatusCode, respBody)
	}
	return nil
}

// Balance returns the aggregated balance of one account for one asset.
// The v2 API takes the account filter as a query-DSL request body.
func (c *Client) Balance(ctx context.Context, account, asset string) (*big.Int, error) {
	u := fmt.Sprintf("%s/v2/%s/aggregate/balances", c.baseURL, url.PathEscape(c.ledger))
	filter, err := json.Marshal(map[string]any{
		"$match": map[string]any{"address": account},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, bytes.NewReader(filter))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("aggregate balances: status %d: %s", resp.StatusCode, respBody)
	}
	var out struct {
		Data map[string]*big.Int `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if b, ok := out.Data[asset]; ok {
		return b, nil
	}
	return big.NewInt(0), nil
}
