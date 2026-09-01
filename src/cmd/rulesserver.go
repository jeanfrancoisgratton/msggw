// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"msggw/internal/config"
	"msggw/internal/rulesproto"
)

// errRemoteRulesDisabled distinguishes "this tenant does not support remote
// rules management" (403 — a config choice) from a bad credential (401),
// the same way errRemotePairingDisabled does for pairing.
var errRemoteRulesDisabled = errors.New("remote rules management is not enabled for this user")

// rulesServer serves the daemon side of self-service routing-rule
// management (see docs/RUNNING.md, "Remote rules management"): a user's own
// client fetches and replaces their own routing.default_direct,
// routing.default_group and routing.rules over the listener, instead of an
// operator hand-editing config.json or running "msg-gw rules".
//
// Like pairServer, it runs for the whole process lifetime and reads cfg
// through an atomic pointer rather than a snapshot — see daemon.go,
// startPersistentListener.
type rulesServer struct {
	cfg            *atomic.Pointer[config.Config]
	log            *slog.Logger
	reloadRequests chan<- reloadRequest

	// mu serializes config.Mutate calls made through this server, so two
	// concurrent pushes queue instead of racing Mutate's optimistic
	// read-before-write check. A push racing the CLI's own "msg-gw rules"
	// command is a separate process and still falls back to that check —
	// unchanged, existing behavior for any two concurrent config.Mutate
	// callers.
	mu sync.Mutex
}

func newRulesServer(cfg *atomic.Pointer[config.Config], log *slog.Logger, reloadRequests chan<- reloadRequest) *rulesServer {
	return &rulesServer{cfg: cfg, log: log, reloadRequests: reloadRequests}
}

// mount registers this server's routes onto mux, alongside whatever else
// the listener already serves (/healthz, /pair/...).
func (s *rulesServer) mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /rules/{name}", s.handleGet)
	mux.HandleFunc("PUT /rules/{name}", s.handlePut)
}

func (s *rulesServer) authenticate(r *http.Request, cfg *config.Config, user config.UserConfig) error {
	return authenticateBearerToken(r, user.RemoteRules.TokenRef, cfg.Vault, errRemoteRulesDisabled)
}

func (s *rulesServer) denyAuth(w http.ResponseWriter, user config.UserConfig, err error) {
	denyAuth(w, s.log, "remote rules", errRemoteRulesDisabled, user, err)
}

// handleGet answers GET /rules/{name} with name's current routing rules, so
// a client can fetch-then-edit rather than reconstructing them from memory
// before pushing a full replacement.
func (s *rulesServer) handleGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	name := r.PathValue("name")
	user, err := findUser(cfg, name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.authenticate(r, cfg, user); err != nil {
		s.denyAuth(w, user, err)
		return
	}

	writeJSON(w, http.StatusOK, rulesproto.RulesDocument{
		DefaultDirect: user.Routing.DefaultDirect,
		DefaultGroup:  user.Routing.DefaultGroup,
		Rules:         user.Routing.Rules,
	})
}

// handlePut answers PUT /rules/{name}: it validates, saves and — unlike the
// CLI's "msg-gw rules add/remove", which leaves picking up the change to a
// separate "msg-gw reload" — synchronously reloads the daemon before
// responding, so the caller gets an honest pass/fail rather than having to
// guess whether their change is actually live. It replaces routing.rules
// only — default_direct/default_group are operator-set and cannot be
// changed here at all (see rulesproto.RulesUpdate), so the fallback
// destination that guarantees message delivery when nothing in Rules
// matches can never be dropped by a user's own push.
func (s *rulesServer) handlePut(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	name := r.PathValue("name")
	user, err := findUser(cfg, name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.authenticate(r, cfg, user); err != nil {
		s.denyAuth(w, user, err)
		return
	}

	body := http.MaxBytesReader(w, r.Body, 256*1024)
	var doc rulesproto.RulesUpdate
	if err := json.NewDecoder(body).Decode(&doc); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decoding request: %w", err))
		return
	}
	if err := config.ValidateRuleList(doc.Rules); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}

	s.mu.Lock()
	_, err = config.Mutate(cfg.Path(), func(c *config.Config) error {
		for i := range c.Users {
			if c.Users[i].Name != name {
				continue
			}
			// default_direct/default_group are deliberately untouched —
			// see rulesproto.RulesUpdate.
			c.Users[i].Routing.Rules = doc.Rules
			return nil
		}
		return fmt.Errorf("no user named %q in %s", name, cfg.Path())
	})
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}

	respCh := make(chan error, 1)
	req := reloadRequest{
		source:  "rules push: " + name,
		respond: func(err error) { respCh <- err },
	}
	select {
	case s.reloadRequests <- req:
	case <-r.Context().Done():
		return
	}

	select {
	case err := <-respCh:
		if err != nil {
			// config.Mutate above already committed the change to
			// config.json — this is "saved, not yet live", not a
			// rejection. See rulesclient.ErrNotApplied.
			s.log.Warn("rules push saved but the daemon could not apply it", "user", name, "error", err)
			writeJSON(w, http.StatusAccepted, rulesproto.ErrorResponse{Error: err.Error(), Applied: false})
			return
		}
	case <-r.Context().Done():
		return
	}

	s.log.Info("rules replaced via remote push", "user", name, "remote_addr", r.RemoteAddr, "rules", len(doc.Rules))
	writeJSON(w, http.StatusOK, rulesproto.RulesDocument{
		DefaultDirect: user.Routing.DefaultDirect, // unchanged by this push
		DefaultGroup:  user.Routing.DefaultGroup,  // unchanged by this push
		Rules:         doc.Rules,
	})
}
