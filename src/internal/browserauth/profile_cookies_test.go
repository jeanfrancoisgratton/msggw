// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package browserauth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/browserutils/kooky"
)

type fakeBrowser struct {
	browser, profile string
	isDefault        bool
	path             string
}

func (f fakeBrowser) Browser() string        { return f.browser }
func (f fakeBrowser) Profile() string        { return f.profile }
func (f fakeBrowser) IsDefaultProfile() bool { return f.isDefault }
func (f fakeBrowser) FilePath() string       { return f.path }

func TestIsGoogleDomain(t *testing.T) {
	tests := []struct {
		domain string
		want   bool
	}{
		{"google.com", true},
		{".google.com", true},
		{"messages.google.com", true}, // where OSID actually lives
		{"accounts.google.com", true},
		{"evilgoogle.com", false}, // ends with "google.com" as a raw substring, but isn't a subdomain
		{"notgoogle.com", false},  // same trap
		{"google.com.evil.com", false},
		{"example.com", false},
	}
	for _, tt := range tests {
		if got := isGoogleDomain(tt.domain); got != tt.want {
			t.Errorf("isGoogleDomain(%q) = %v, want %v", tt.domain, got, tt.want)
		}
	}
}

func fakeCookie(name, value string, b kooky.BrowserInfo) *kooky.Cookie {
	return &kooky.Cookie{
		Cookie:  http.Cookie{Name: name, Value: value, Domain: "google.com"},
		Browser: b,
	}
}

// withReadCookies swaps the package-level readCookies seam for the duration
// of a test, so ReadProfileCookies's grouping/validation logic can be
// exercised without touching a real browser profile or OS keyring.
func withReadCookies(t *testing.T, fn func(ctx context.Context) (kooky.Cookies, error)) {
	t.Helper()
	prev := readCookies
	readCookies = fn
	t.Cleanup(func() { readCookies = prev })
}

func TestReadProfileCookies_NoCookiesFound(t *testing.T) {
	withReadCookies(t, func(context.Context) (kooky.Cookies, error) {
		return nil, nil
	})

	_, _, err := ReadProfileCookies(context.Background())
	if !errors.Is(err, ErrNoProfileSession) {
		t.Fatalf("got err %v, want ErrNoProfileSession", err)
	}
}

func TestReadProfileCookies_UnderlyingErrorWithNoCookies(t *testing.T) {
	wantErr := errors.New("keyring unavailable")
	withReadCookies(t, func(context.Context) (kooky.Cookies, error) {
		return nil, wantErr
	})

	_, _, err := ReadProfileCookies(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("got err %v, want it to wrap %v", err, wantErr)
	}
}

func TestReadProfileCookies_CompleteProfileWins(t *testing.T) {
	def := fakeBrowser{browser: "chrome", isDefault: true, path: "/home/u/.config/google-chrome/Default/Cookies"}

	withReadCookies(t, func(context.Context) (kooky.Cookies, error) {
		return kooky.Cookies{
			fakeCookie("SID", "sid-val", def),
			fakeCookie("HSID", "hsid-val", def),
			fakeCookie("SSID", "ssid-val", def),
			fakeCookie("OSID", "osid-val", def),
			fakeCookie("APISID", "apisid-val", def),
			fakeCookie("SAPISID", "sapisid-val", def),
			fakeCookie("__Secure-1PSIDTS", "extra-val", def),
		}, nil
	})

	cookies, source, err := ReadProfileCookies(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != "chrome" {
		t.Errorf("got source %q, want %q", source, "chrome")
	}
	if cookies["SID"] != "sid-val" || cookies["__Secure-1PSIDTS"] != "extra-val" {
		t.Errorf("got cookies %v, missing expected values", cookies)
	}
}

func TestReadProfileCookies_NamesNonDefaultProfile(t *testing.T) {
	prof := fakeBrowser{browser: "chrome", profile: "Profile 1", isDefault: false, path: "/home/u/.config/google-chrome/Profile 1/Cookies"}

	withReadCookies(t, func(context.Context) (kooky.Cookies, error) {
		return kooky.Cookies{
			fakeCookie("SID", "v", prof),
			fakeCookie("HSID", "v", prof),
			fakeCookie("SSID", "v", prof),
			fakeCookie("OSID", "v", prof),
			fakeCookie("APISID", "v", prof),
			fakeCookie("SAPISID", "v", prof),
		}, nil
	})

	_, source, err := ReadProfileCookies(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != "chrome (Profile 1)" {
		t.Errorf("got source %q, want %q", source, "chrome (Profile 1)")
	}
}

func TestReadProfileCookies_DoesNotMixCookiesAcrossProfiles(t *testing.T) {
	a := fakeBrowser{browser: "chrome", isDefault: true, path: "/profile/a/Cookies"}
	b := fakeBrowser{browser: "chromium", isDefault: true, path: "/profile/b/Cookies"}

	withReadCookies(t, func(context.Context) (kooky.Cookies, error) {
		return kooky.Cookies{
			// a has SID/HSID/SSID only; b has the rest. Neither is complete
			// on its own, and they must not be merged into one session.
			fakeCookie("SID", "v", a),
			fakeCookie("HSID", "v", a),
			fakeCookie("SSID", "v", a),
			fakeCookie("OSID", "v", b),
			fakeCookie("APISID", "v", b),
			fakeCookie("SAPISID", "v", b),
		}, nil
	})

	_, _, err := ReadProfileCookies(context.Background())
	if !errors.Is(err, ErrNoProfileSession) {
		t.Fatalf("got err %v, want ErrNoProfileSession (no single complete profile)", err)
	}
}

func TestReadProfileCookies_SkipsIncompleteProfileForCompleteOne(t *testing.T) {
	incomplete := fakeBrowser{browser: "edge", isDefault: true, path: "/profile/incomplete/Cookies"}
	complete := fakeBrowser{browser: "chrome", isDefault: true, path: "/profile/complete/Cookies"}

	withReadCookies(t, func(context.Context) (kooky.Cookies, error) {
		return kooky.Cookies{
			fakeCookie("SID", "v", incomplete),
			fakeCookie("HSID", "v", incomplete),
			fakeCookie("SID", "v", complete),
			fakeCookie("HSID", "v", complete),
			fakeCookie("SSID", "v", complete),
			fakeCookie("OSID", "v", complete),
			fakeCookie("APISID", "v", complete),
			fakeCookie("SAPISID", "v", complete),
		}, nil
	})

	cookies, source, err := ReadProfileCookies(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != "chrome" {
		t.Errorf("got source %q, want %q", source, "chrome")
	}
	if len(cookies) != 6 {
		t.Errorf("got %d cookies, want 6", len(cookies))
	}
}
