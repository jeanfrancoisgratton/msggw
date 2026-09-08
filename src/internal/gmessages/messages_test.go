// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/gmessages/messages_test.go

package gmessages

import (
	"testing"
	"time"
)

func at(offsetHours int) Message {
	return Message{Timestamp: time.Unix(0, 0).Add(time.Duration(offsetHours) * time.Hour)}
}

// TestMessagesAfterAllWithinWindow covers a page that never crosses cutoff:
// FetchMessagesSince must keep paginating for more.
func TestMessagesAfterAllWithinWindow(t *testing.T) {
	page := []Message{at(5), at(4), at(3)}
	kept, full := messagesAfter(page, time.Unix(0, 0).Add(time.Hour))
	if !full {
		t.Error("full = false, want true: every message in the page is at or after cutoff")
	}
	if len(kept) != len(page) {
		t.Errorf("kept %d messages, want all %d", len(kept), len(page))
	}
}

// TestMessagesAfterCutoffMidPage covers the page where history crosses the
// window: only the prefix newer than cutoff should be kept, and pagination
// should stop.
func TestMessagesAfterCutoffMidPage(t *testing.T) {
	// Cutoff sits strictly between at(3) and at(2): at(3) is exactly at
	// cutoff (kept, "at or after" includes the boundary) and at(2) is
	// before it (dropped, and pagination stops there).
	cutoff := time.Unix(0, 0).Add(3 * time.Hour)
	page := []Message{at(5), at(4), at(3), at(2), at(1)}
	kept, full := messagesAfter(page, cutoff)
	if full {
		t.Error("full = true, want false: the page holds a message older than cutoff")
	}
	if len(kept) != 3 {
		t.Fatalf("kept %d messages, want 3 (at(5), at(4), at(3))", len(kept))
	}
	if kept[0].Timestamp != at(5).Timestamp || kept[1].Timestamp != at(4).Timestamp || kept[2].Timestamp != at(3).Timestamp {
		t.Errorf("kept the wrong messages: %+v", kept)
	}
}

// TestMessagesAfterEmptyPage covers the boundary the caller relies on to stop
// pagination without special-casing it.
func TestMessagesAfterEmptyPage(t *testing.T) {
	kept, full := messagesAfter(nil, time.Now())
	if !full {
		t.Error("full = false for an empty page, want true (vacuously all-kept)")
	}
	if len(kept) != 0 {
		t.Errorf("kept %d messages from an empty page, want 0", len(kept))
	}
}
