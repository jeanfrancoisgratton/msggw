// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

// Package rulesclient is the client side of self-service routing-rule
// management (see docs/RUNNING.md, "Remote rules management"): it lets
// "msg-gw rules pull/push NAME --remote ..." fetch and replace one tenant's
// routing rules over the network, authenticated the same way "pair --remote"
// is, without ever touching the daemon's config.json directly.
package rulesclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"msggw/internal/config"
	"msggw/internal/rulesproto"
)

// ErrNotApplied is returned by Put when the daemon saved the pushed rules to
// config.json but could not switch the running daemon over to them (e.g. a
// transient failure reconnecting to Mattermost). The rules are not lost —
// the daemon reverted to its previous, still-running configuration — but
// they are not live yet either.
var ErrNotApplied = fmt.Errorf("the daemon saved the rules but could not apply them")

// Client talks to one daemon's rules-management endpoint.
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

// Get fetches name's current routing rules.
func (c *Client) Get(ctx context.Context, name string) (rulesproto.RulesDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/rules/"+name, nil)
	if err != nil {
		return rulesproto.RulesDocument{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return rulesproto.RulesDocument{}, fmt.Errorf("GET /rules/%s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return rulesproto.RulesDocument{}, apiError(resp, name)
	}

	var doc rulesproto.RulesDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return rulesproto.RulesDocument{}, fmt.Errorf("decoding response: %w", err)
	}
	return doc, nil
}

// Put replaces name's routing.rules wholesale with rules, blocking until the
// daemon confirms whether the change is live. It cannot change
// default_direct/default_group — those are operator-set; see
// rulesproto.RulesUpdate — so the returned document reflects them unchanged.
// A non-nil error wrapping ErrNotApplied means the rules were saved but the
// daemon could not switch to them yet — see ErrNotApplied.
func (c *Client) Put(ctx context.Context, name string, rules []config.Rule) (rulesproto.RulesDocument, error) {
	payload, err := json.Marshal(rulesproto.RulesUpdate{Rules: rules})
	if err != nil {
		return rulesproto.RulesDocument{}, fmt.Errorf("encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/rules/"+name, bytes.NewReader(payload))
	if err != nil {
		return rulesproto.RulesDocument{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return rulesproto.RulesDocument{}, fmt.Errorf("PUT /rules/%s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return rulesproto.RulesDocument{}, apiError(resp, name)
	}

	var out rulesproto.RulesDocument
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return rulesproto.RulesDocument{}, fmt.Errorf("decoding response: %w", err)
	}
	return out, nil
}

func apiError(resp *http.Response, name string) error {
	data, _ := io.ReadAll(resp.Body)
	var apiErr rulesproto.ErrorResponse
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
		err := fmt.Errorf("%s: %s (%s)", name, apiErr.Error, resp.Status)
		if resp.StatusCode == http.StatusAccepted {
			// The daemon uses 202 specifically for "saved, not yet live" —
			// see rulesserver.go's handlePut.
			return fmt.Errorf("%w: %w", ErrNotApplied, err)
		}
		return err
	}
	return fmt.Errorf("%s: %s", name, resp.Status)
}
