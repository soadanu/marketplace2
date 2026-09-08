package main

import (
	"os"
	"path/filepath"
	"strconv"
)

// dataDir is where the JSON store and uploads live. Set DATA_DIR to the
// mount path of a Render persistent disk (or any durable volume) in

var dataDir = resolveDataDir()

func resolveDataDir() string {
	if v := os.Getenv("DATA_DIR"); v != "" {
		return v
	}
	return "data"
}

// dataPath joins one or more path parts onto the configured data directory.
// Use this everywhere instead of hardcoding "data/..." so DATA_DIR is
// respected consistently.
func dataPath(parts ...string) string {
	all := append([]string{dataDir}, parts...)
	return filepath.Join(all...)
}

// platformCommissionRate is the cut Kaya takes on every sale, e.g. 0.05 for
// 5%. Configurable via PLATFORM_COMMISSION (as a decimal, "0.05" not "5").
// This same rate is set as each seller's subaccount split_value when their
// payout account is created - see payment.go.
func platformCommissionRate() float64 {
	v := os.Getenv("PLATFORM_COMMISSION")
	if v == "" {
		return 0.05
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		return 0.05
	}
	return f
}
