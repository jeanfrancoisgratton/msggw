// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package cmd

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"msggw/internal/config"
	"msggw/internal/pairproto"
)

func testPairServer() *pairServer {
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "hastoken", RemotePairing: config.RemotePairingConfig{TokenRef: "literal:s3cret"}},
			{Name: "notoken"},
		},
	}
	sharedCfg := new(atomic.Pointer[config.Config])
	sharedCfg.Store(cfg)
	return newPairServer(sharedCfg, slog.New(slog.NewTextHandler(discard{}, nil)))
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func newTestRequest(t *testing.T, method, path, token string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling request body: %v", err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestPairServerUnknownUser(t *testing.T) {
	s := testPairServer()
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "POST", "/pair/ghost/start", "s3cret", map[string]string{}))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestPairServerRemotePairingDisabled(t *testing.T) {
	s := testPairServer()
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "POST", "/pair/notoken/start", "anything", map[string]string{}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPairServerWrongToken(t *testing.T) {
	s := testPairServer()
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "POST", "/pair/hastoken/start", "wrong", map[string]string{}))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestPairServerMissingToken(t *testing.T) {
	s := testPairServer()
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "POST", "/pair/hastoken/start", "", map[string]string{}))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestPairServerMissingCookies(t *testing.T) {
	s := testPairServer()
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "POST", "/pair/hastoken/start", "s3cret", map[string]string{"SID": "x"}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var apiErr pairproto.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&apiErr); err != nil {
		t.Fatalf("decoding error body: %v", err)
	}
	if !strings.Contains(apiErr.Error, "HSID") {
		t.Errorf("error %q does not name the missing cookies", apiErr.Error)
	}
}

func TestPairServerUnknownPairingID(t *testing.T) {
	s := testPairServer()
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "POST", "/pair/hastoken/wait", "s3cret", pairproto.WaitRequest{PairingID: "nope"}))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
