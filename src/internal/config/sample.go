// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:44:09
// Original filename: src/internal/config/sample.go

package config

import _ "embed"

//go:embed config.sample.json
var sample string

// Sample returns a commented-by-example configuration file, for
// "rcs-mm_gw config sample > /etc/rcs_gateway/config.json".
func Sample() string { return sample }
