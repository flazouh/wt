package liveness

import "testing"

// A busy slot-17 must not make slot-1 look busy (lesson L030).
func TestMentionsPathMatchesWholePath(t *testing.T) {
	cases := []struct {
		candidate, path string
		want            bool
	}{
		{"/wt/slot-1", "/wt/slot-1", true},
		{"/wt/slot-1/src", "/wt/slot-1", true},
		{"vim /wt/slot-1/a.go", "/wt/slot-1", true},
		{"/wt/slot-17", "/wt/slot-1", false},
		{"cd /wt/slot-17 && make", "/wt/slot-1", false},
		{"/wt/slot-17 /wt/slot-1", "/wt/slot-1", true},
	}
	for _, c := range cases {
		if got := mentionsPath(c.candidate, c.path); got != c.want {
			t.Errorf("mentionsPath(%q, %q) = %v, want %v", c.candidate, c.path, got, c.want)
		}
	}
}
