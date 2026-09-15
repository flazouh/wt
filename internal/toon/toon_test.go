package toon

import "testing"

func TestTableCarriesItsOwnCount(t *testing.T) {
	var d Doc
	d.Table("slots", []string{"index", "branch"}, [][]any{
		{1, "main"},
		{2, "feature/x"},
	})

	want := "slots[2]{index,branch}:\n  1,main\n  2,feature/x\n"
	if got := d.String(); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// An empty table must still render its header, or the agent cannot tell a zero
// from a missing field and calls again to check.
func TestAnEmptyTableStillStatesTheZero(t *testing.T) {
	var d Doc
	d.Table("slots", []string{"index"}, nil)

	if got := d.String(); got != "slots[0]{index}:\n" {
		t.Fatalf("got %q", got)
	}
}

func TestQuotesOnlyWhatWouldBeMisread(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "plain"},
		{"feature/x", "feature/x"},
		{"/a/path-01", "/a/path-01"},
		{"has,comma", `"has,comma"`},
		{"has: colon", `"has: colon"`},
		{"", `""`},
		{"a\nb", `"a\nb"`},
		{`say "hi"`, `"say \"hi\""`},
	} {
		if got := scalar(tc.in); got != tc.want {
			t.Errorf("scalar(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSectionOrdersItsFields(t *testing.T) {
	var d Doc
	d.Section("pool", map[string]any{"used": 2, "limit": 5, "repo": "/r"})

	want := "pool:\n  limit: 5\n  repo: /r\n  used: 2\n"
	if got := d.String(); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestHelpIsOmittedWhenThereIsNothingToSuggest(t *testing.T) {
	var d Doc
	d.Field("ok", true).Help()

	if got := d.String(); got != "ok: true\n" {
		t.Fatalf("got %q, want no help block", got)
	}
}
