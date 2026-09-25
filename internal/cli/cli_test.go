package cli

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := Run(args, &out)
	return out.String(), code
}

func TestVersionAnswersOnEverySpelling(t *testing.T) {
	for _, flag := range []string{"-v", "-V", "--version", "version"} {
		out, code := run(t, flag)
		if code != OK {
			t.Errorf("%s exited %d", flag, code)
		}
		if strings.TrimSpace(out) != Version {
			t.Errorf("%s printed %q, want the bare version", flag, out)
		}
	}
}

// An unknown flag must be rejected by name, not dropped. A dropped flag hands
// the agent output it believes is scoped when it is not.
func TestUnknownFlagIsRejectedAndSelfCorrecting(t *testing.T) {
	out, code := run(t, "strays", "--aply")

	if code != Usage {
		t.Fatalf("exit %d, want %d", code, Usage)
	}
	if !strings.Contains(out, "--aply") {
		t.Errorf("the error does not name the flag: %q", out)
	}
	// Per AXI: fold the --help lookup into the error so the fix is one turn.
	if !strings.Contains(out, "--apply") {
		t.Errorf("the error does not list the valid flags: %q", out)
	}
}

func TestUnknownCommandListsTheRealOnes(t *testing.T) {
	out, code := run(t, "aquire")

	if code != Usage {
		t.Fatalf("exit %d, want %d", code, Usage)
	}
	for _, name := range []string{"take", "done", "drop", "strays"} {
		if !strings.Contains(out, name) {
			t.Errorf("the error omits the %q command: %q", name, out)
		}
	}
}

// Errors are data on stdout, not stack traces on stderr: the agent reads one
// and not the other.
func TestErrorsGoToStdoutAsStructuredOutput(t *testing.T) {
	out, _ := run(t, "take")

	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("output does not start with a structured error: %q", out)
	}
	if !strings.Contains(out, "help[") {
		t.Errorf("the error carries no suggestion: %q", out)
	}
}

func TestTakeWithoutABranchSaysSo(t *testing.T) {
	out, code := run(t, "take")

	if code != Usage {
		t.Fatalf("exit %d, want %d", code, Usage)
	}
	if !strings.Contains(out, "branch") {
		t.Errorf("the error does not name what is missing: %q", out)
	}
}

func TestHelpIsPerCommandNotTheWholeManual(t *testing.T) {
	out, code := run(t, "take", "--help")

	if code != OK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "wt take") {
		t.Errorf("help does not identify the command: %q", out)
	}
	if strings.Contains(out, "enforce") {
		t.Errorf("per-command help leaked the whole manual: %q", out)
	}
}

func TestTopLevelHelpIdentifiesTheTool(t *testing.T) {
	out, code := run(t, "--help")

	if code != OK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"bin:", "description:", "commands["} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q: %q", want, out)
		}
	}
}

func TestOpposingFlagsAreRefusedRatherThanGuessed(t *testing.T) {
	out, code := run(t, "enforce", "--install", "--uninstall")

	if code != Usage {
		t.Fatalf("exit %d, want %d", code, Usage)
	}
	if !strings.Contains(out, "opposites") {
		t.Errorf("the error does not explain the conflict: %q", out)
	}
}

func TestFlagValuesAreAcceptedInBothForms(t *testing.T) {
	for _, args := range [][]string{
		{"take", "b", "--owner", "codex"},
		{"take", "b", "--owner=codex"},
	} {
		flags, rest, err := parse(args[1:], commands["take"].flags)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if flags["owner"] != "codex" {
			t.Errorf("%v gave owner %q", args, flags["owner"])
		}
		if len(rest) != 1 || rest[0] != "b" {
			t.Errorf("%v lost the branch: %v", args, rest)
		}
	}
}

func TestAValueFlagWithNoValueIsAnError(t *testing.T) {
	if _, _, err := parse([]string{"--owner"}, commands["take"].flags); err == nil {
		t.Fatal("--owner with no value was accepted")
	}
}

func TestArchiveIsAFlagStraysKnowsAbout(t *testing.T) {
	flags, _, err := parse([]string{"--archive", "--apply"}, commands["strays"].flags)
	if err != nil {
		t.Fatalf("strays --archive --apply: %v", err)
	}
	for _, name := range []string{"archive", "apply"} {
		if flags[name] != "true" {
			t.Errorf("--%s did not survive parsing: %v", name, flags)
		}
	}
}

// The two modes are not opposites, so both must be reachable from the one
// error an agent is most likely to see first.
func TestStraysHelpOffersBothSweeps(t *testing.T) {
	out, code := run(t, "strays", "--help")

	if code != OK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"--apply", "--archive"} {
		if !strings.Contains(out, want) {
			t.Errorf("help omits %s: %q", want, out)
		}
	}
}

func TestLimitFromEnv(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		ok    bool
	}{
		{"", 0, true},
		{"10", 10, true},
		{" 7 ", 7, true},
		{"0", 0, false},
		{"-3", 0, false},
		{"ten", 0, false},
	} {
		t.Setenv("WT_LIMIT", tc.value)
		got, err := limitFromEnv()
		if (err == nil) != tc.ok {
			t.Errorf("WT_LIMIT=%q: err %v, want ok=%v", tc.value, err, tc.ok)
			continue
		}
		if got != tc.want {
			t.Errorf("WT_LIMIT=%q: got %d, want %d", tc.value, got, tc.want)
		}
	}
}
