package proc

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

var started = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

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
		if p.Start.IsZero() {
			p.Start = started
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

func TestHolderPrefersTheNearestAgentOverTheTerminalAboveIt(t *testing.T) {
	// `ori claude` launches claude from a terminal shell. The session is the
	// claude process; the shell outlives any one session.
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

// A script between the person and wt exits long before the work is done, so
// the lease belongs to the terminal that ran it.
func TestHolderLooksPastAScriptToTheTerminal(t *testing.T) {
	table := chain(
		Process{PID: 400, Name: "bash", Args: "bash ./release.sh"},
		Process{PID: 300, Name: "make", Args: "make release"},
		Process{PID: 200, Name: "zsh", Args: "zsh -l"},
	)

	got, ok := table.Holder(400)

	if !ok || got.PID != 200 {
		t.Fatalf("holder is %+v, want the terminal shell 200", got)
	}
}

func TestHolderTreatsEveryCommandShellAsThrowaway(t *testing.T) {
	for _, args := range []string{
		"bash -lc wt take x",
		"bash -l -c wt take x",
		"bash --login -c wt take x",
		"bash --noprofile -c wt take x",
		"sh -c -- wt take x",
	} {
		t.Run(args, func(t *testing.T) {
			table := chain(
				Process{PID: 300, Name: "bash", Args: args},
				Process{PID: 200, Name: "codex", Args: "codex exec"},
			)

			got, ok := table.Holder(300)

			if !ok || got.PID != 200 {
				t.Fatalf("holder is %+v, want codex 200", got)
			}
		})
	}
}

// When nothing above wt is an agent or a terminal, no holder is recorded and
// the lease keeps the two-day rule. Guessing would release it early.
func TestHolderIsUnknownWithoutAnAgentOrATerminal(t *testing.T) {
	for name, table := range map[string]Table{
		"only throwaway shells": chain(
			Process{PID: 300, Name: "zsh", Args: "zsh -c wt take x"},
			Process{PID: 200, Name: "env", Args: "env FOO=1 zsh -c wt take x"},
		),
		"an unlisted agent under launchd": chain(
			Process{PID: 300, Name: "zsh", Args: "zsh -c wt take x"},
			Process{PID: 200, Name: "node", Args: "node agent.js"},
		),
		// ps could not show the shell's command line, so there is no knowing
		// whether it runs one command or waits on a person.
		"a shell with no command line": chain(
			Process{PID: 300, Name: "zsh", Args: ""},
			Process{PID: 200, Name: "node", Args: "node agent.js"},
		),
		"an empty table": {},
		"a cycle": {
			10: {PID: 10, PPID: 20, Name: "zsh", Args: "zsh -c x"},
			20: {PID: 20, PPID: 10, Name: "zsh", Args: "zsh -c y"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := table.Holder(300); ok {
				t.Fatalf("holder is %+v, want none", got)
			}
			if got, ok := table.Holder(10); ok {
				t.Fatalf("holder is %+v, want none", got)
			}
		})
	}
}

func TestAliveNeedsTheSamePIDAndTheSameStart(t *testing.T) {
	table := Table{200: {PID: 200, Start: started, Name: "claude"}}

	if !table.Alive(200, started) {
		t.Error("the recorded process is running, want alive")
	}
	if !table.Alive(200, started.Add(StartTolerance)) {
		t.Error("a start read a clock step apart is the same process, want alive")
	}
	if table.Alive(200, started.Add(-time.Hour)) {
		t.Error("the PID was reused by a later process, want gone")
	}
	if table.Alive(201, started) {
		t.Error("no process has that PID, want gone")
	}
}

func TestParseReadsNamesWithSpacesAndJoinsArgs(t *testing.T) {
	starts := "  200     1 Sun Sep 27 10:00:00 2026 Claude Helper\n" +
		"  300   200 Thu Oct  1 09:05:00 2026 zsh\n" +
		"  400   200 not a date at all x\n" +
		"garbage\n"
	args := "  200 /Applications/Claude.app/Contents/MacOS/Claude Helper --type=gpu\n" +
		"  300 /bin/zsh -c wt take x\n"

	table, err := parse(starts, args)
	if err != nil {
		t.Fatal(err)
	}

	want := Process{PID: 200, PPID: 1, Start: started, Name: "Claude Helper",
		Args: "/Applications/Claude.app/Contents/MacOS/Claude Helper --type=gpu"}
	if table[200] != want {
		t.Errorf("200 is %+v, want %+v", table[200], want)
	}
	if p := table[300]; p.PPID != 200 || p.Args != "/bin/zsh -c wt take x" ||
		!p.Start.Equal(time.Date(2026, 10, 1, 9, 5, 0, 0, time.UTC)) {
		t.Errorf("300 is %+v", p)
	}
	if len(table) != 2 {
		t.Errorf("table has %d rows, want the 2 well-formed ones", len(table))
	}
}

func TestParseRefusesAnEmptyReading(t *testing.T) {
	// "ps printed nothing" must never become "every holder is dead".
	if _, err := parse("", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an empty process table parsed with %v, want ErrUnavailable", err)
	}
}

func TestReadSeesThisProcess(t *testing.T) {
	table, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if p := table[os.Getpid()]; p.Start.IsZero() || p.Args == "" {
		t.Fatalf("this process reads as %+v, want its start and command line", p)
	}
}

// The start must be the same instant whatever zone the reader runs in. An
// agent under launchd often runs in UTC while the person runs in theirs.
func TestAStartReadsTheSameInEveryTimeZone(t *testing.T) {
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	pid := sleeper.Process.Pid

	var starts []time.Time
	for _, zone := range []string{"UTC", "Europe/Paris", "America/Los_Angeles"} {
		t.Setenv("TZ", zone)
		table, err := Read()
		if err != nil {
			t.Fatal(err)
		}
		p, ok := table[pid]
		if !ok {
			t.Fatalf("TZ=%s: ps did not list the sleeper %s", zone, strconv.Itoa(pid))
		}
		starts = append(starts, p.Start)
	}
	for _, s := range starts[1:] {
		if !s.Equal(starts[0]) {
			t.Fatalf("the start moved with the time zone: %v", starts)
		}
	}
}
