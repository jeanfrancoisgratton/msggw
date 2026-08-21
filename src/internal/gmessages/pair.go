// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:01:47
// Original filename: src/internal/gmessages/pair.go

package gmessages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// qrExpiry is how long a pairing QR code stays valid. It is Google's window,
// not ours: after it, the code has to be regenerated.
const qrExpiry = 30 * time.Second

// ErrPairingTimeout is returned when the QR code was never scanned.
var ErrPairingTimeout = errors.New("the QR code was not scanned in time")

// Pairing drives the QR pairing flow: the daemon shows a QR code, the operator
// scans it from Google Messages on the phone under Settings → Device pairing,
// and the phone hands back the session credentials.
//
// The Google-account flow libgm also offers is not used here. It needs cookies
// lifted from a signed-in browser session, which is not something a headless
// daemon can obtain on its own.
type Pairing struct {
	client *Client
	paired chan *gmproto.PairedData
	events <-chan Event
}

// NewPairing starts a pairing session on a fresh, unpaired client.
func NewPairing(cfg Config) *Pairing {
	p := &Pairing{
		client: NewUnpaired(cfg),
		paired: make(chan *gmproto.PairedData, 1),
	}

	// Installing a callback takes libgm's default post-pairing behaviour out
	// of the picture — no PairSuccessful event, no automatic reconnect — so
	// that the session is persisted before anything else happens to it.
	callback := func(data *gmproto.PairedData) {
		select {
		case p.paired <- data:
		default:
		}
	}
	p.client.gm.PairCallback.Store(&callback)

	return p
}

// Start returns the URL to encode as a QR code for the phone to scan.
func (p *Pairing) Start(ctx context.Context) (string, error) {
	if err := p.client.gm.FetchConfig(ctx); err != nil {
		return "", fmt.Errorf("fetching the Google Messages client config: %w", err)
	}
	qr, err := p.client.gm.StartLogin()
	if err != nil {
		return "", fmt.Errorf("starting the pairing: %w", err)
	}
	return qr, nil
}

// Refresh issues a new QR code after the previous one expired.
func (p *Pairing) Refresh() (string, error) {
	qr, err := p.client.gm.RefreshPhoneRelay()
	if err != nil {
		return "", fmt.Errorf("refreshing the pairing QR code: %w", err)
	}
	return qr, nil
}

// Wait blocks until the phone completes the pairing, the QR code expires, or
// ctx is cancelled. On expiry it returns ErrPairingTimeout, and the caller is
// expected to call Refresh and Wait again.
func (p *Pairing) Wait(ctx context.Context) (phoneID string, err error) {
	timer := time.NewTimer(qrExpiry)
	defer timer.Stop()

	select {
	case data := <-p.paired:
		phoneID = data.GetMobile().GetSourceID()
		p.client.session.SetPhoneID(phoneID)
		if err := p.client.saveSession(); err != nil {
			return "", fmt.Errorf("the phone paired, but the session could not be stored: %w", err)
		}
		return phoneID, nil

	case <-timer.C:
		return "", ErrPairingTimeout

	case <-ctx.Done():
		return "", ctx.Err()
	}
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
