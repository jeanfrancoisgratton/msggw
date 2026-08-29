// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

// Package pairclient is the client side of "client mode" pairing (see
// docs/MULTI-TENANCY.md): it lets "msg-gw pair NAME --remote ..." run
// entirely on the operator's own device — signing into Google there — and
// hand the resulting cookies to a daemon over the network, rather than a
// human copying them onto the daemon's host by hand.
package pairclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"msggw/internal/pairproto"
)

// Client talks to one daemon's remote-pairing endpoint.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a Client. baseURL is the daemon's listener address, e.g.
// "https://msggw.example.net:8443". insecureSkipVerify disables TLS
// certificate verification, for testing against a self-signed listener.
func New(baseURL, token string, insecureSkipVerify bool) *Client {
	c := &http.Client{}
	if insecureSkipVerify {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, http: c}
}

// Start registers cookies against tenant name and returns the emoji to
// confirm on the phone.
func (c *Client) Start(ctx context.Context, name string, cookies map[string]string) (pairproto.StartResponse, error) {
	var resp pairproto.StartResponse
	err := c.do(ctx, "/pair/"+name+"/start", cookies, &resp)
	return resp, err
}

// Wait blocks until the daemon reports the phone confirmed pairingID, or ctx
// is cancelled. The daemon bounds how long it itself waits on Google's side,
// so this call is expected to take as long as pairing does, not to return
// immediately.
func (c *Client) Wait(ctx context.Context, name, pairingID string) (pairproto.WaitResponse, error) {
	var resp pairproto.WaitResponse
	err := c.do(ctx, "/pair/"+name+"/wait", pairproto.WaitRequest{PairingID: pairingID}, &resp)
	return resp, err
}

func (c *Client) do(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		var apiErr pairproto.ErrorResponse
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("POST %s: %s (%s)", path, apiErr.Error, resp.Status)
		}
		return fmt.Errorf("POST %s: %s", path, resp.Status)
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
