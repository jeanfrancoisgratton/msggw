// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/config/mutate.go

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mutate reads the configuration at path, applies fn to it, and atomically
// replaces the file with the result — but only once the mutated
// configuration has been proven to load and validate cleanly by running it
// back through Load. A mutation can therefore never leave config.json in a
// state the daemon would refuse to start with, and fn does not need to
// duplicate Load's validation rules.
//
// fn receives the configuration exactly as decoded from disk, before
// applyDefaults runs — a value the operator already wrote is preserved
// exactly, and nothing gets expanded into an explicit default just because
// the file was rewritten.
//
// One caveat, inherent to encoding/json rather than something Mutate works
// around: it can omit a field at its zero value for a string, slice or
// pointer, but not for a plain struct, so an optional object-typed field
// that was entirely absent before (routing.default_group, remote_pairing) —
// on a user Mutate doesn't even touch, since the whole file is re-encoded —
// may appear as an empty object afterward. A configuration that already sets
// its top-level sections, which any deployment following the sample does,
// is unaffected: only a genuinely-unset optional block can be turned into
// an empty one, never a value that was actually there.
//
// Mutate detects a change to path made by someone else between its read and
// its write and fails rather than silently discarding it; the correct
// response is to re-run the command against the current file.
//
// On success, Mutate returns the new configuration as Load would return it
// (defaults applied, fully validated) — the shape callers that go on to log
// or print it expect.
func Mutate(path string, fn func(*Config) error) (*Config, error) {
	before, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading configuration %s: %w", path, err)
	}

	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(before)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing configuration %s: %w", path, err)
	}
	cfg.path = path

	if err := fn(&cfg); err != nil {
		return nil, err
	}

	after, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the updated configuration: %w", err)
	}
	after = append(after, '\n')

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".msggw-config-*.json")
	if err != nil {
		return nil, fmt.Errorf("creating a temporary file next to %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(after); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return nil, fmt.Errorf("setting permissions on %s: %w", tmpPath, err)
	}

	// Proving the candidate is loadable is what makes Mutate safe to build a
	// CLI on: a mutation that would leave routing rules unparsable, a
	// destination invalid, or anything else Load checks, is rejected here —
	// before it ever touches the real file — rather than surfacing as a
	// daemon startup failure discovered on the next restart.
	validated, err := Load(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("this change would leave the configuration invalid: %w", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("re-reading %s before committing: %w", path, err)
	}
	if string(current) != string(before) {
		return nil, fmt.Errorf("%s changed on disk while this command was running; re-run it against the current file", path)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("replacing %s: %w", path, err)
	}
	validated.path = path

	return validated, nil
}
