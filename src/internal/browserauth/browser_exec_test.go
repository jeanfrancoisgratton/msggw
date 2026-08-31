// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package browserauth

import (
	"errors"
	"testing"
)

var errNotFound = errors.New("not found")

func lookupNone(string) (string, error) { return "", errNotFound }
func statNone(string) error             { return errNotFound }

func lookupOnly(match, result string) func(string) (string, error) {
	return func(name string) (string, error) {
		if name == match {
			return result, nil
		}
		return "", errNotFound
	}
}

func statOnly(match string) func(string) error {
	return func(path string) error {
		if path == match {
			return nil
		}
		return errNotFound
	}
}

func TestFindBrowser(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		lookup func(string) (string, error)
		stat   func(string) error
		want   string
		wantOK bool
	}{
		{
			name:   "windows: chrome on PATH",
			goos:   "windows",
			lookup: lookupOnly("chrome", `C:\Users\jf\chrome.exe`),
			stat:   statNone,
			want:   `C:\Users\jf\chrome.exe`,
			wantOK: true,
		},
		{
			name:   "windows: no chrome, edge on PATH",
			goos:   "windows",
			lookup: lookupOnly("msedge", `C:\Users\jf\msedge.exe`),
			stat:   statNone,
			want:   `C:\Users\jf\msedge.exe`,
			wantOK: true,
		},
		{
			name:   "windows: no chrome, edge at fixed Program Files path",
			goos:   "windows",
			lookup: lookupNone,
			stat:   statOnly(`C:\Program Files\Microsoft\Edge\Application\msedge.exe`),
			want:   `C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			wantOK: true,
		},
		{
			name:   "windows: nothing found",
			goos:   "windows",
			lookup: lookupNone,
			stat:   statNone,
			wantOK: false,
		},
		{
			name:   "darwin: chrome app bundle",
			goos:   "darwin",
			lookup: lookupNone,
			stat:   statOnly("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
			want:   "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			wantOK: true,
		},
		{
			name:   "darwin: no chrome, edge app bundle",
			goos:   "darwin",
			lookup: lookupNone,
			stat:   statOnly("/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"),
			want:   "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			wantOK: true,
		},
		{
			name:   "darwin: nothing found",
			goos:   "darwin",
			lookup: lookupNone,
			stat:   statNone,
			wantOK: false,
		},
		{
			name:   "linux: chromium on PATH",
			goos:   "linux",
			lookup: lookupOnly("chromium", "/usr/bin/chromium"),
			stat:   statNone,
			want:   "/usr/bin/chromium",
			wantOK: true,
		},
		{
			name:   "linux: nothing found",
			goos:   "linux",
			lookup: lookupNone,
			stat:   statNone,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findBrowser(tt.goos, tt.lookup, tt.stat)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("findBrowser() unexpected error: %v", err)
				}
				if got != tt.want {
					t.Fatalf("findBrowser() = %q, want %q", got, tt.want)
				}
				return
			}
			if !errors.Is(err, ErrBrowserNotFound) {
				t.Fatalf("findBrowser() error = %v, want ErrBrowserNotFound", err)
			}
		})
	}
}
