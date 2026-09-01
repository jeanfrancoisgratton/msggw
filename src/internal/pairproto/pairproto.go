// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

// Package pairproto is the wire format shared between the daemon's
// remote-pairing HTTP endpoint (wired up in cmd/pairserver.go) and msg-gw's
// own "pair --remote" client (internal/pairclient) — see
// docs/SOLUTION.md, "client mode".
package pairproto

// StartResponse answers POST /pair/{name}/start. The request body is the
// cookie JSON object itself — the same shape "pair --cookies-file" reads.
type StartResponse struct {
	// PairingID identifies this attempt for the follow-up call to Wait.
	PairingID string `json:"pairing_id"`
	// Emoji is what to show the operator to tap on the phone.
	Emoji string `json:"emoji"`
}

// WaitRequest is the body of POST /pair/{name}/wait.
type WaitRequest struct {
	PairingID string `json:"pairing_id"`
}

// WaitResponse answers POST /pair/{name}/wait once the phone has confirmed
// and the daemon has verified the session by reconnecting with it.
type WaitResponse struct {
	PhoneID       string `json:"phone_id"`
	Conversations int    `json:"conversations"`
}

// ErrorResponse is the body of any non-200 response.
type ErrorResponse struct {
	Error string `json:"error"`
}
