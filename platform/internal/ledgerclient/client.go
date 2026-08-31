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

// Balance returns the aggregated balance of one account for one asset.
func (c *Client) Balance(ctx context.Context, account, asset string) (*big.Int, error) {
	u := fmt.Sprintf("%s/v2/%s/aggregate/balances?address=%s", c.baseURL,
		url.PathEscape(c.ledger), url.QueryEscape(account))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
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
