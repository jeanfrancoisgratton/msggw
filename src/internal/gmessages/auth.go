// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:48:12
// Original filename: src/internal/gmessages/auth.go

package gmessages

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"

	"rcs_gateway/internal/secrets"
)

// ErrNotPaired is returned when the daemon is asked to connect but no session
// has been stored yet.
var ErrNotPaired = errors.New("not paired with Google Messages: run \"rcs-mm_gw pair\" first")

// session is what gets persisted between runs. It is versioned because
// libgm's AuthData is a reverse-engineered structure that changes with the
// protocol; a marker lets a future release recognise an old session instead of
// failing to unmarshal it in some confusing way.
type session struct {
	Version  int             `json:"version"`
	AuthData json.RawMessage `json:"auth_data"`
	// PhoneID is the paired phone's source ID, kept for the status command.
	PhoneID string `json:"phone_id,omitempty"`
}

const sessionVersion = 1

// SessionStore persists the Google Messages session through a secrets.Store.
//
// It exists because the session is not a static credential: libgm refreshes
// its Tachyon auth token roughly hourly and mutates AuthData in place, and a
// daemon that never writes the new token back re-pairs on every restart.
type SessionStore struct {
	store secrets.Store

	mu      sync.Mutex
	phoneID string
}

// NewSessionStore wraps a secrets.Store.
func NewSessionStore(store secrets.Store) *SessionStore {
	return &SessionStore{store: store}
}

// Describe returns a loggable description of where the session lives.
func (s *SessionStore) Describe() string { return s.store.Describe() }

// Load returns the stored AuthData, or ErrNotPaired when there is none.
func (s *SessionStore) Load() (*libgm.AuthData, error) {
	raw, err := s.store.Load()
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, ErrNotPaired
	}
	if err != nil {
		return nil, fmt.Errorf("reading session from %s: %w", s.store.Describe(), err)
	}
	if len(raw) == 0 {
		return nil, ErrNotPaired
	}

	var sess session
	if err := json.Unmarshal(raw, &sess); err != nil {
		// An encoded: store uses unauthenticated AES-CFB, so a wrong passphrase
		// produces garbage rather than an error. This is where that shows up.
		return nil, fmt.Errorf("session in %s is not readable — a wrong passphrase would look like this: %w",
			s.store.Describe(), err)
	}
	if sess.Version != sessionVersion {
		return nil, fmt.Errorf("session in %s is version %d, this build reads version %d: re-pair with \"rcs-mm_gw pair\"",
			s.store.Describe(), sess.Version, sessionVersion)
	}

	var auth libgm.AuthData
	if err := json.Unmarshal(sess.AuthData, &auth); err != nil {
		return nil, fmt.Errorf("session in %s has unreadable auth data: %w", s.store.Describe(), err)
	}

	s.mu.Lock()
	s.phoneID = sess.PhoneID
	s.mu.Unlock()

	return &auth, nil
}

// Save writes the session back.
func (s *SessionStore) Save(auth *libgm.AuthData) error {
	authJSON, err := json.Marshal(auth)
	if err != nil {
		return fmt.Errorf("encoding session: %w", err)
	}

	s.mu.Lock()
	phoneID := s.phoneID
	s.mu.Unlock()

	raw, err := json.Marshal(session{
		Version:  sessionVersion,
		AuthData: authJSON,
		PhoneID:  phoneID,
	})
	if err != nil {
		return fmt.Errorf("encoding session: %w", err)
	}

	if err := s.store.Save(raw); err != nil {
		return fmt.Errorf("writing session to %s: %w", s.store.Describe(), err)
	}
	return nil
}

// SetPhoneID records which phone this session is paired with. It takes effect
// on the next Save.
func (s *SessionStore) SetPhoneID(id string) {
	s.mu.Lock()
	s.phoneID = id
	s.mu.Unlock()
}

// PhoneID returns the paired phone's ID, which is only populated after a Load
// or a pairing.
func (s *SessionStore) PhoneID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phoneID
}

// Clear removes the stored session, so that the daemon is unpaired locally.
func (s *SessionStore) Clear() error {
	if err := s.store.Save(nil); err != nil {
		return fmt.Errorf("clearing session in %s: %w", s.store.Describe(), err)
	}
	return nil
}
