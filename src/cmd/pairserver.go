// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package cmd

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"msggw/internal/config"
	"msggw/internal/gmessages"
	"msggw/internal/pairproto"
	"msggw/internal/secrets"
)

// pendingPairingTTL bounds how long a started-but-never-waited-on pairing
// stays open. Past this, the server assumes the client crashed or gave up
// and tears the connection to Google down rather than leaking it.
const pendingPairingTTL = 5 * time.Minute

// errRemotePairingDisabled and errUnauthorized distinguish "this tenant does
// not support remote pairing" (403 — a config choice) from "the token given
// does not match" (401 — a bad credential), rather than collapsing both into
// one generic denial.
var (
	errRemotePairingDisabled = errors.New("remote pairing is not enabled for this user")
	errUnauthorized          = errors.New("missing or invalid bearer token")
)

// pairServer serves the daemon side of "client mode" pairing (see
// docs/MULTI-TENANCY.md): a client running msg-gw on the operator's own
// device signs into Google there and hands the resulting cookies to this
// endpoint, instead of a human copying them onto the daemon's host.
type pairServer struct {
	cfg *config.Config
	log *slog.Logger

	mu      sync.Mutex
	pending map[string]*pendingPairing
}

type pendingPairing struct {
	user    string
	pairing *gmessages.Pairing
	expires time.Time
}

func newPairServer(cfg *config.Config, log *slog.Logger) *pairServer {
	return &pairServer{cfg: cfg, log: log, pending: make(map[string]*pendingPairing)}
}

// mount registers this server's routes onto mux, alongside whatever else the
// listener already serves (/healthz).
func (s *pairServer) mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /pair/{name}/start", s.handleStart)
	mux.HandleFunc("POST /pair/{name}/wait", s.handleWait)
}

// sweep closes pairings whose client never called wait — see
// pendingPairingTTL — until ctx is cancelled, at which point it closes
// whatever is still outstanding and returns.
func (s *pairServer) sweep(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for id, p := range s.pending {
				p.pairing.Close()
				delete(s.pending, id)
			}
			s.mu.Unlock()
			return
		case now := <-ticker.C:
			s.mu.Lock()
			for id, p := range s.pending {
				if now.After(p.expires) {
					p.pairing.Close()
					delete(s.pending, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *pairServer) handleStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	user, err := findUser(s.cfg, name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.authenticate(r, user); err != nil {
		s.denyAuth(w, user, err)
		return
	}

	body := http.MaxBytesReader(w, r.Body, 64*1024)
	var cookies map[string]string
	if err := json.NewDecoder(body).Decode(&cookies); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decoding cookies: %w", err))
		return
	}
	if err := gmessages.ValidateCookies(cookies); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	gmCfg, err := newGMessagesConfig(user, s.cfg, s.log)
	if err != nil {
		s.log.Error("remote pairing: could not build the gmessages config", "user", name, "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}

	pairing := gmessages.NewPairing(gmCfg)
	emoji, err := pairing.Start(r.Context(), cookies)
	if err != nil {
		pairing.Close()
		writeError(w, http.StatusBadGateway, err)
		return
	}

	id := uuid.NewString()
	s.mu.Lock()
	s.pending[id] = &pendingPairing{user: name, pairing: pairing, expires: time.Now().Add(pendingPairingTTL)}
	s.mu.Unlock()

	s.log.Info("remote pairing started", "user", name, "remote_addr", r.RemoteAddr)
	writeJSON(w, http.StatusOK, pairproto.StartResponse{PairingID: id, Emoji: emoji})
}

func (s *pairServer) handleWait(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	user, err := findUser(s.cfg, name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.authenticate(r, user); err != nil {
		s.denyAuth(w, user, err)
		return
	}

	body := http.MaxBytesReader(w, r.Body, 4*1024)
	var req pairproto.WaitRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decoding request: %w", err))
		return
	}

	s.mu.Lock()
	entry, ok := s.pending[req.PairingID]
	if ok {
		delete(s.pending, req.PairingID)
	}
	s.mu.Unlock()
	if !ok || entry.user != name {
		writeError(w, http.StatusNotFound, errors.New("unknown or expired pairing_id"))
		return
	}
	defer entry.pairing.Close()

	phoneID, err := entry.pairing.Wait(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	conversations, err := entry.pairing.Verify(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("the pairing was stored, but reconnecting with it failed: %w", err))
		return
	}

	s.log.Info("remote pairing confirmed", "user", name, "phone_id", phoneID)
	writeJSON(w, http.StatusOK, pairproto.WaitResponse{PhoneID: phoneID, Conversations: len(conversations)})
}

// authenticate checks the request's bearer token against user's configured
// remote_pairing.token_ref. Body parsing never happens before this succeeds,
// so an unauthenticated caller cannot make the daemon do anything but a
// cheap string compare.
func (s *pairServer) authenticate(r *http.Request, user config.UserConfig) error {
	if user.RemotePairing.TokenRef == "" {
		return errRemotePairingDisabled
	}
	want, err := secrets.OpenString(user.RemotePairing.TokenRef, s.cfg.Vault)
	if err != nil {
		return fmt.Errorf("resolving remote_pairing.token_ref: %w", err)
	}
	if want == "" {
		return errRemotePairingDisabled
	}

	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return errUnauthorized
	}
	got := strings.TrimPrefix(auth, prefix)
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return errUnauthorized
	}
	return nil
}

// denyAuth maps an authenticate error onto the right HTTP status, logging
// anything unexpected (a broken token_ref) without leaking Vault or file
// details to the network caller.
func (s *pairServer) denyAuth(w http.ResponseWriter, user config.UserConfig, err error) {
	switch {
	case errors.Is(err, errRemotePairingDisabled):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, errUnauthorized):
		writeError(w, http.StatusUnauthorized, err)
	default:
		s.log.Error("remote pairing: could not check the token", "user", user.Name, "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, pairproto.ErrorResponse{Error: err.Error()})
}
