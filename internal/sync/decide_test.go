package sync

import "testing"

func TestDecide(t *testing.T) {
	for name, tc := range map[string]struct {
		local, drive, base string
		want               Action
	}{
		"both unchanged":          {"a", "a", "a", ActionNothing},
		"local changed":           {"b", "a", "a", ActionUpload},
		"drive changed":           {"a", "b", "a", ActionDownload},
		"both changed, same":      {"b", "b", "a", ActionUpdateBase},
		"both changed, different": {"b", "c", "a", ActionConflict},
		"both created, no base":   {"b", "c", "", ActionConflict},
	} {
		if got := Decide(tc.local, tc.drive, tc.base); got != tc.want {
			t.Errorf("%s: Decide = %v, want %v", name, got, tc.want)
		}
	}
}
