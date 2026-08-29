// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:01:47
// Original filename: src/internal/gmessages/pair.go

package gmessages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
)

// RequiredCookies is Google's own minimum set for the Gaia pairing handshake;
// __Secure-1PSIDTS is usually present too but is not load-bearing the way
// these are. Shared by the local "pair" command and the remote-pairing HTTP
// endpoint, so both reject a short cookie set the same way.
var RequiredCookies = []string{"SID", "HSID", "SSID", "OSID", "APISID", "SAPISID"}

// ValidateCookies reports an error naming whichever of RequiredCookies is
// missing from cookies, so a caller can fail before spending a round-trip to
// Google on a cookie set that was never going to work.
func ValidateCookies(cookies map[string]string) error {
	var missing []string
	for _, name := range RequiredCookies {
		if cookies[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required cookies: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Pairing errors, re-exported from libgm so that callers do not need to
// import it directly — everything that knows about the Google Messages
// protocol is meant to stay behind this package.
var (
	ErrNoCookies          = libgm.ErrNoCookies
	ErrNoDevicesFound     = libgm.ErrNoDevicesFound
	ErrIncorrectEmoji     = libgm.ErrIncorrectEmoji
	ErrPairingCancelled   = libgm.ErrPairingCancelled
	ErrPairingTimeout     = libgm.ErrPairingTimeout
	ErrPairingInitTimeout = libgm.ErrPairingInitTimeout
	ErrHadMultipleDevices = libgm.ErrHadMultipleDevices
)

// Pairing drives the Google-account pairing flow: the operator supplies
// cookies lifted from a signed-in browser session, the daemon shows an
// emoji, and tapping it on the Google Messages app on the phone hands back
// the session credentials.
//
// QR pairing — scanning a code shown here from Google Messages' own device
// pairing screen — is gone. Google retired the endpoint it depended on;
// cookies plus an emoji confirmation is the only pairing method libgm still
// has that works.
type Pairing struct {
	client *Client
	sess   *libgm.PairingSession
	events <-chan Event
}

// NewPairing starts a pairing session on a fresh, unpaired client.
func NewPairing(cfg Config) *Pairing {
	return &Pairing{client: NewUnpaired(cfg)}
}

// Start authenticates with cookies lifted from a signed-in Google Messages
// for web browser session and returns the emoji to confirm on the phone.
func (p *Pairing) Start(ctx context.Context, cookies map[string]string) (emoji string, err error) {
	p.client.gm.AuthData.SetCookies(cookies)
	if err := p.client.gm.FetchConfig(ctx); err != nil {
		return "", fmt.Errorf("fetching the Google Messages client config: %w", err)
	}
	emoji, sess, err := p.client.gm.StartGaiaPairing(ctx)
	if err != nil {
		return "", fmt.Errorf("starting the pairing: %w", err)
	}
	p.sess = sess
	return emoji, nil
}

// Wait blocks until the phone confirms the emoji, or ctx is cancelled.
func (p *Pairing) Wait(ctx context.Context) (phoneID string, err error) {
	if _, err := p.client.gm.FinishGaiaPairing(ctx, p.sess); err != nil {
		return "", fmt.Errorf("finishing the pairing: %w", err)
	}

	phoneID = p.client.gm.AuthData.Mobile.GetSourceID()
	p.client.session.SetPhoneID(phoneID)
	if err := p.client.saveSession(); err != nil {
		return "", fmt.Errorf("the phone paired, but the session could not be stored: %w", err)
	}
	return phoneID, nil
}

// Verify reconnects with the freshly stored session and waits for the phone to
// send its conversation list, which is the success criterion docs/SOLUTION.md
// sets for phase 1.
//
// The pause before reconnecting is libgm's own advice: the phone needs a
// moment to save the pairing, and reconnecting into that window gets the
// session unpaired again.
func (p *Pairing) Verify(ctx context.Context) ([]Conversation, error) {
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if err := p.client.gm.Reconnect(); err != nil {
		return nil, fmt.Errorf("reconnecting after pairing: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case evt, ok := <-p.eventsOnce():
			if !ok {
				return nil, errors.New("the connection closed before the phone sent its conversations")
			}
			if ready, isReady := evt.(ReadyEvent); isReady {
				// The reconnect refreshed the auth token, so store it before
				// the process exits.
				if err := p.client.saveSession(); err != nil {
					return nil, fmt.Errorf("storing the refreshed session: %w", err)
				}
				return ready.Conversations, nil
			}
		}
	}
}

// Close tears the pairing session down.
func (p *Pairing) Close() { p.client.Disconnect() }

// eventsOnce memoises the event channel, since Events starts a new pump on
// every call and Verify reads it in a loop.
func (p *Pairing) eventsOnce() <-chan Event {
	if p.events == nil {
		p.events = p.client.Events()
	}
	return p.events
}
