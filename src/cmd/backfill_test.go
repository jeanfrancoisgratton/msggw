// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/backfill_test.go

package cmd

import "testing"

// TestSetBackfillOnlyTouchesChangedFields covers the "only the flag(s) you
// pass are changed" behaviour: setting --count must not clobber an unrelated
// --days value, and vice versa.
func TestSetBackfillOnlyTouchesChangedFields(t *testing.T) {
	cfg := writePairTestConfig(t)

	newCfg, err := setBackfill(cfg, "existing", true, 14, false, 0)
	if err != nil {
		t.Fatalf("setBackfill (days only): %v", err)
	}
	user, err := findUser(newCfg, "existing")
	if err != nil {
		t.Fatalf("findUser: %v", err)
	}
	if got := user.GMessages.BackfillDaysCount(); got != 14 {
		t.Errorf("BackfillDaysCount() = %d, want 14", got)
	}
	if user.GMessages.BackfillCount != 0 {
		t.Errorf("BackfillCount = %d, want 0 (untouched)", user.GMessages.BackfillCount)
	}

	newCfg, err = setBackfill(newCfg, "existing", false, 0, true, 25)
	if err != nil {
		t.Fatalf("setBackfill (count only): %v", err)
	}
	user, err = findUser(newCfg, "existing")
	if err != nil {
		t.Fatalf("findUser: %v", err)
	}
	if got := user.GMessages.BackfillDaysCount(); got != 14 {
		t.Errorf("BackfillDaysCount() = %d, want 14 (untouched by the --count-only call)", got)
	}
	if user.GMessages.BackfillCount != 25 {
		t.Errorf("BackfillCount = %d, want 25", user.GMessages.BackfillCount)
	}
}

// TestSetBackfillExplicitZeroDisablesDays covers that an explicit --days 0 is
// stored as a real 0, not left as "unset" (which would default back to 7).
func TestSetBackfillExplicitZeroDisablesDays(t *testing.T) {
	cfg := writePairTestConfig(t)

	newCfg, err := setBackfill(cfg, "existing", true, 0, false, 0)
	if err != nil {
		t.Fatalf("setBackfill: %v", err)
	}
	user, err := findUser(newCfg, "existing")
	if err != nil {
		t.Fatalf("findUser: %v", err)
	}
	if got := user.GMessages.BackfillDaysCount(); got != 0 {
		t.Errorf("BackfillDaysCount() = %d, want 0", got)
	}
}

// TestSetBackfillUnknownUser covers the same "reject before touching disk"
// guarantee "msg-gw rules" gives.
func TestSetBackfillUnknownUser(t *testing.T) {
	cfg := writePairTestConfig(t)

	if _, err := setBackfill(cfg, "nobody", true, 14, false, 0); err == nil {
		t.Fatal("setBackfill for an unknown user: want an error, got nil")
	}
}
