// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

// Package browserauth automates the one interactive step pairing can't
// script away: proving ownership of a Google account. It drives a locally
// installed Chrome/Chromium/Edge over the DevTools protocol so the pairing
// command never has to ask its user to open devtools or hand-copy cookies
// into a JSON file — they just sign in like they would for any other app.
package browserauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"msggw/internal/gmessages"
)

// ErrBrowserNotFound means no Chrome/Chromium/Edge install could be located
// on this machine, so the caller should fall back to --cookies-file or
// --no-browser.
var ErrBrowserNotFound = errors.New(
	"could not find an installed Chrome, Chromium, or Edge browser to automate sign-in")

// ErrSignInTimeout means the browser window opened but nobody signed in
// within signInTimeout.
var ErrSignInTimeout = errors.New("timed out waiting for Google sign-in")

const (
	signInURL     = "https://messages.google.com/web"
	pollInterval  = 2 * time.Second
	signInTimeout = 5 * time.Minute
)

// CaptureCookies opens a visible browser window at Google Messages' web
// sign-in, waits for the user to sign into their Google account, and
// returns once gmessages.RequiredCookies are all present. ctx bounds the
// whole operation (e.g. via signal.NotifyContext in cmd/pair.go); closing it
// closes the browser and cleans up its temporary profile directory.
func CaptureCookies(ctx context.Context, out io.Writer) (map[string]string, error) {
	browserPath, err := findBrowser(runtime.GOOS, exec.LookPath, statFile)
	if err != nil {
		return nil, err
	}

	profileDir, err := os.MkdirTemp("", "msggw-pair-*")
	if err != nil {
		return nil, fmt.Errorf("creating a temporary browser profile: %w", err)
	}
	defer os.RemoveAll(profileDir)

	fmt.Fprintln(out, "Opening a browser window so you can sign into your Google account (this can take a few seconds)...")

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx,
		chromedp.ExecPath(browserPath),
		chromedp.UserDataDir(profileDir),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-extensions", true),
	)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	if err := chromedp.Run(browserCtx, chromedp.Navigate(signInURL)); err != nil {
		return nil, fmt.Errorf("opening %s: %w", signInURL, err)
	}

	fmt.Fprintln(out, "Sign into your Google account in the window that opened. This closes automatically once you're signed in.")

	timeoutCtx, cancel := context.WithTimeout(ctx, signInTimeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrSignInTimeout
		case <-ticker.C:
			cookies, err := pollCookies(browserCtx)
			if err != nil {
				return nil, fmt.Errorf("reading cookies from the browser: %w", err)
			}
			if gmessages.ValidateCookies(cookies) == nil {
				fmt.Fprintln(out, "Signed in.")
				return cookies, nil
			}
		}
	}
}

// pollCookies reads whatever Google account cookies the browser currently
// holds for google.com and its messages.google.com subdomain.
func pollCookies(ctx context.Context) (map[string]string, error) {
	var raw []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		raw, err = network.GetCookies().
			WithURLs([]string{"https://google.com", "https://messages.google.com"}).
			Do(ctx)
		return err
	}))
	if err != nil {
		return nil, err
	}

	cookies := make(map[string]string, len(raw))
	for _, c := range raw {
		cookies[c.Name] = c.Value
	}
	return cookies, nil
}

func statFile(path string) error {
	_, err := os.Stat(path)
	return err
}
