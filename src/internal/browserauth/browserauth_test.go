// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package browserauth

import (
	"testing"

	"msggw/internal/gmessages"
)

// TestCookieCaptureURLsIncludesMessagesGoogleCom guards against silently
// dropping messages.google.com from the automated capture flow: OSID (one of
// gmessages.RequiredCookies) is scoped to that domain specifically and never
// appears under plain google.com, so losing this URL would make automated
// pairing (CaptureCookies) fail validation every time, not just occasionally.
func TestCookieCaptureURLsIncludesMessagesGoogleCom(t *testing.T) {
	found := false
	for _, u := range cookieCaptureURLs {
		if u == "https://messages.google.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("cookieCaptureURLs %v does not include https://messages.google.com, "+
			"which is where OSID (%v) actually lives", cookieCaptureURLs, gmessages.RequiredCookies)
	}
}
