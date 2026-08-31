// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package browserauth

// findBrowser locates an installed Chrome/Chromium/Edge browser to drive.
//
// chromedp's own default detection (used when no ExecPath option is given)
// only looks for Chrome and Chromium — never Edge, on any platform. That's a
// problem here specifically: Windows ships Edge but not Chrome, and the
// whole point of msggw's Windows .msi build is reaching non-technical users
// who are unlikely to have installed Chrome themselves. So this checks for
// Edge explicitly wherever a Chromium-family browser isn't otherwise
// bundled by default (Windows, macOS).
//
// lookup and stat are exec.LookPath and a path-exists check, injected so
// this can be unit-tested without touching a real filesystem or PATH.
func findBrowser(goos string, lookup func(string) (string, error), stat func(string) error) (string, error) {
	for _, name := range lookupNames(goos) {
		if path, err := lookup(name); err == nil {
			return path, nil
		}
	}
	for _, path := range fixedPaths(goos) {
		if err := stat(path); err == nil {
			return path, nil
		}
	}
	return "", ErrBrowserNotFound
}

func lookupNames(goos string) []string {
	switch goos {
	case "windows":
		return []string{"chrome", "chrome.exe", "msedge", "msedge.exe"}
	case "darwin":
		return nil
	default:
		return []string{
			"chromium", "chromium-browser",
			"google-chrome", "google-chrome-stable",
			"microsoft-edge", "microsoft-edge-stable",
		}
	}
}

func fixedPaths(goos string) []string {
	switch goos {
	case "windows":
		return []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		}
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	default:
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/microsoft-edge",
			"/usr/bin/microsoft-edge-stable",
			"/snap/bin/chromium",
		}
	}
}
