package proc

import "testing"

// chain builds a table from child to root: each entry's parent is the next
// one, and the last one's parent is launchd.
func chain(procs ...Process) Table {
	t := Table{}
	for i, p := range procs {
		if i+1 < len(procs) {
			p.PPID = procs[i+1].PID
		} else {
			p.PPID = 1
		}
		if p.Start == "" {
			p.Start = "Sun Sep 27 10:00:00 2026"
		}
		t[p.PID] = p
	}
	return t
}

func TestHolderSkipsTheAgentsThrowawayShell(t *testing.T) {
	// What Claude Code's Bash tool looks like: every command runs in a fresh
	// `zsh -c` that exits with it. Recording that shell would make every lease
	// look abandoned seconds after it was taken.
	table := chain(
		Process{PID: 300, Name: "zsh", Args: "/bin/zsh -c source snapshot.sh && wt take x"},
		Process{PID: 200, Name: "claude", Args: "claude --output-format stream-json"},
		Process{PID: 100, Name: "zsh", Args: "-zsh"},
	)

	got, ok := table.Holder(300)

	if !ok || got.PID != 200 {
		t.Fatalf("holder is %+v, want the claude process 200", got)
	}
}

func TestHolderPrefersTheNearestAgentOverAWrapperThatLooksDurable(t *testing.T) {
	// `ori claude` launches claude as a child. Both are long-lived, but the
	// session is the claude process: ori outlives any one session.
	table := chain(
		Process{PID: 400, Name: "zsh", Args: "/bin/zsh -c wt take x"},
		Process{PID: 300, Name: "rtk", Args: "rtk wt take x"},
		Process{PID: 200, Name: "claude", Args: "claude --settings {}"},
		Process{PID: 150, Name: "ori", Args: "ori claude"},
		Process{PID: 100, Name: "zsh", Args: "-zsh"},
	)

	got, ok := table.Holder(400)

	if !ok || got.PID != 200 {
		t.Fatalf("holder is %+v, want the claude process 200", got)
	}
}

func TestHolderIsTheInteractiveShellForAPerson(t *testing.T) {
	table := chain(
		Process{PID: 200, Name: "zsh", Args: "-zsh"},
		Process{PID: 100, Name: "stable", Args: "/Applications/Warp.app/Contents/MacOS/stable"},
	)

	got, ok := table.Holder(200)

	if !ok || got.PID != 200 {
		t.Fatalf("holder is %+v, want the interactive shell 200", got)
	}
}

func TestHolderTreatsClusteredCommandFlagsAsThrowaway(t *testing.T) {
	table := chain(
		Process{PID: 300, Name: "bash", Args: "bash -lc wt take x"},
		Process{PID: 200, Name: "codex", Args: "codex exec"},
	)

	got, ok := table.Holder(300)

	if !ok || got.PID != 200 {
		t.Fatalf("holder is %+v, want codex 200", got)
	}
}

func TestHolderIsUnknownWhenOnlyThrowawayProcessesAreFound(t *testing.T) {
	table := chain(
		Process{PID: 300, Name: "zsh", Args: "zsh -c wt take x"},
		Process{PID: 200, Name: "env", Args: "env FOO=1 zsh -c wt take x"},
	)

	if got, ok := table.Holder(300); ok {
		t.Fatalf("holder is %+v, want none", got)
	}
}

func TestHolderIsUnknownWhenTheStartIsMissing(t *testing.T) {
	if got, ok := (Table{}).Holder(300); ok {
		t.Fatalf("holder is %+v from an empty table, want none", got)
	}
}

func TestHolderSurvivesACycle(t *testing.T) {
	table := Table{
		10: {PID: 10, PPID: 20, Name: "zsh", Args: "zsh -c x", Start: "a"},
		20: {PID: 20, PPID: 10, Name: "zsh", Args: "zsh -c y", Start: "b"},
	}

	if got, ok := table.Holder(10); ok {
		t.Fatalf("holder is %+v, want none", got)
	}
}

func TestAliveNeedsTheSamePIDAndTheSameStart(t *testing.T) {
	table := Table{200: {PID: 200, Start: "Sun Sep 27 10:00:00 2026", Name: "claude"}}

	if !table.Alive(200, "Sun Sep 27 10:00:00 2026") {
		t.Error("the recorded process is running, want alive")
	}
	if table.Alive(200, "Sat Sep 26 09:00:00 2026") {
		t.Error("the PID was reused by a later process, want gone")
	}
	if table.Alive(201, "Sun Sep 27 10:00:00 2026") {
		t.Error("no process has that PID, want gone")
	}
}

func TestParseReadsNamesWithSpacesAndJoinsArgs(t *testing.T) {
	starts := "  200     1 Sun Sep 27 10:00:00 2026 Claude Helper\n" +
		"  300   200 Sun Sep 27 10:05:00 2026 zsh\n" +
		"garbage\n"
	args := "  200 /Applications/Claude.app/Contents/MacOS/Claude Helper --type=gpu\n" +
		"  300 /bin/zsh -c wt take x\n"

	table, err := parse(starts, args)
	if err != nil {
		t.Fatal(err)
	}

	want := Process{PID: 200, PPID: 1, Start: "Sun Sep 27 10:00:00 2026", Name: "Claude Helper",
		Args: "/Applications/Claude.app/Contents/MacOS/Claude Helper --type=gpu"}
	if table[200] != want {
		t.Errorf("200 is %+v, want %+v", table[200], want)
	}
	if table[300].PPID != 200 || table[300].Args != "/bin/zsh -c wt take x" {
		t.Errorf("300 is %+v", table[300])
	}
	if len(table) != 2 {
		t.Errorf("table has %d rows, want the 2 well-formed ones", len(table))
	}
}

func TestParseRefusesAnEmptyReading(t *testing.T) {
	// "ps printed nothing" must never become "every owner is dead".
	if _, err := parse("", ""); err == nil {
		t.Fatal("an empty process table parsed without error")
	}
}

func TestReadSeesThisProcess(t *testing.T) {
	table, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(table) < 2 {
		t.Fatalf("read %d processes, want the machine's table", len(table))
	}
}
