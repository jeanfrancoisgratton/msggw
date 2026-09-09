// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/backfill_test.go

package cmd

import "testing"

// TestSetBackfillChangesCount covers the basic case: setBackfill updates
// BackfillCount for the named user.
func TestSetBackfillChangesCount(t *testing.T) {
	cfg := writePairTestConfig(t)

	newCfg, err := setBackfill(cfg, "existing", 25)
	if err != nil {
		t.Fatalf("setBackfill: %v", err)
	}
	user, err := findUser(newCfg, "existing")
	if err != nil {
		t.Fatalf("findUser: %v", err)
	}
	if user.GMessages.BackfillCount != 25 {
		t.Errorf("BackfillCount = %d, want 25", user.GMessages.BackfillCount)
	}
}

// TestSetBackfillExplicitZeroDisables covers that an explicit --count 0 is
// stored as a real 0, disabling backfill.
func TestSetBackfillExplicitZeroDisables(t *testing.T) {
	cfg := writePairTestConfig(t)

	newCfg, err := setBackfill(cfg, "existing", 0)
	if err != nil {
		t.Fatalf("setBackfill: %v", err)
	}
	user, err := findUser(newCfg, "existing")
	if err != nil {
		t.Fatalf("findUser: %v", err)
	}
	if user.GMessages.BackfillCount != 0 {
		t.Errorf("BackfillCount = %d, want 0", user.GMessages.BackfillCount)
	}
}

// TestSetBackfillUnknownUser covers the same "reject before touching disk"
// guarantee "msg-gw rules" gives.
func TestSetBackfillUnknownUser(t *testing.T) {
	cfg := writePairTestConfig(t)

	if _, err := setBackfill(cfg, "nobody", 14); err == nil {
		t.Fatal("setBackfill for an unknown user: want an error, got nil")
	}
}
