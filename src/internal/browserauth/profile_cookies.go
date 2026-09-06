// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package browserauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/browserutils/kooky"
	_ "github.com/browserutils/kooky/browser/chrome"
	_ "github.com/browserutils/kooky/browser/chromium"
	_ "github.com/browserutils/kooky/browser/edge"

	"msggw/internal/gmessages"
)

// secureSessionCookie is not in gmessages.RequiredCookies (pairing works
// without it) but is worth carrying along when present, same as the manual
// cookies fallback documented in "pair --help" and docs/RUNNING.md.
const secureSessionCookie = "__Secure-1PSIDTS"

// profileScanTimeout bounds ReadProfileCookies: this is meant to be a fast,
// silent shortcut, not something pairing should sit waiting on if a
// keyring/D-Bus call on the host hangs.
const profileScanTimeout = 5 * time.Second

// ErrNoProfileSession means no locally installed browser had a complete,
// unexpired Google session cookie set already — the caller should fall back
// to CaptureCookies (or another fallback) instead.
var ErrNoProfileSession = errors.New("no signed-in Google session found in any local browser profile")

// wantedCookieNames is gmessages.RequiredCookies plus secureSessionCookie,
// as a set — the cookie names ReadProfileCookies looks for.
func wantedCookieNames() map[string]bool {
	names := make(map[string]bool, len(gmessages.RequiredCookies)+1)
	for _, name := range gmessages.RequiredCookies {
		names[name] = true
	}
	names[secureSessionCookie] = true
	return names
}

// readCookies is swapped out in tests so ReadProfileCookies's grouping and
// validation logic can be exercised without touching real browser profiles
// or an OS keyring.
var readCookies = func(ctx context.Context) (kooky.Cookies, error) {
	wanted := wantedCookieNames()
	nameAndDomain := kooky.FilterFunc(func(c *kooky.Cookie) bool {
		return wanted[c.Name] && strings.HasSuffix(strings.TrimPrefix(c.Domain, "."), "google.com")
	})
	return kooky.ReadCookies(ctx, nameAndDomain, kooky.Valid)
}

// ReadProfileCookies looks for an already signed-in Google session in a
// locally installed Chrome, Chromium, or Edge profile, without opening any
// window or asking for interaction — the machine either already has one or
// it doesn't. It exists so that pairing on a machine where the operator (or
// a user) already uses Google Messages web in their everyday browser needs
// no human step at all, unlike CaptureCookies, which always drives a fresh,
// visible sign-in.
//
// Cookies from different profiles are never mixed — a session's SID, HSID,
// etc. must all come from the same signed-in profile — so this groups by
// source profile and returns the first one that has gmessages.RequiredCookies
// complete. It returns ErrNoProfileSession if no profile qualifies, so the
// caller can fall back to CaptureCookies.
func ReadProfileCookies(ctx context.Context) (map[string]string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, profileScanTimeout)
	defer cancel()

	cookies, err := readCookies(ctx)
	if len(cookies) == 0 {
		if err != nil {
			return nil, "", fmt.Errorf("scanning local browser profiles for a Google session: %w", err)
		}
		return nil, "", ErrNoProfileSession
	}

	type profile struct {
		cookies map[string]string
		source  string
	}
	byProfile := make(map[string]*profile)
	for _, c := range cookies {
		key := c.Browser.FilePath()
		p, ok := byProfile[key]
		if !ok {
			p = &profile{
				cookies: make(map[string]string),
				source:  describeBrowser(c.Browser),
			}
			byProfile[key] = p
		}
		p.cookies[c.Name] = c.Value
	}

	for _, p := range byProfile {
		if gmessages.ValidateCookies(p.cookies) == nil {
			return p.cookies, p.source, nil
		}
	}
	return nil, "", ErrNoProfileSession
}

func describeBrowser(b kooky.BrowserInfo) string {
	if b == nil {
		return "a local browser profile"
	}
	if b.IsDefaultProfile() {
		return b.Browser()
	}
	return fmt.Sprintf("%s (%s)", b.Browser(), b.Profile())
}
