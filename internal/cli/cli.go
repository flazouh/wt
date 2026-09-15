// Package cli is the surface agents touch. It follows the AXI conventions:
// TOON on stdout, errors as data rather than as stack traces, no prompts, and
// a bare invocation that shows live state instead of a manual.
package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/alexdepape/wt/internal/toon"
)

// Version is the tool's version. It lives here, away from anything that opens a
// file or runs a subprocess, so `wt --version` stays a cheap probe.
const Version = "1.0.0"

// Exit codes, per AXI: 0 success including no-ops, 1 failure, 2 usage.
const (
	OK    = 0
	Fail  = 1
	Usage = 2
)

// Run dispatches one invocation and returns the process exit code.
func Run(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		return home(stdout)
	}

	switch args[0] {
	case "-v", "-V", "--version", "version":
		fmt.Fprintln(stdout, Version)
		return OK
	case "-h", "--help", "help":
		return usage(stdout)
	}

	cmd, ok := commands[args[0]]
	if !ok {
		return errorf(stdout, Usage,
			fmt.Sprintf("unknown command %q", args[0]),
			"valid commands: "+strings.Join(commandNames(), ", "),
			"run `wt` to see the pool")
	}

	flags, rest, err := parse(args[1:], cmd.flags)
	if err != nil {
		return errorf(stdout, Usage, err.Error(), cmd.helpLine())
	}
	if _, wants := flags["help"]; wants {
		return cmd.help(stdout)
	}
	return cmd.run(stdout, flags, rest)
}

// command is one subcommand. Each declares its own flags so an unknown one is
// rejected by name rather than silently dropped: a flag that vanishes gives the
// agent output it believes is filtered when it is not.
type command struct {
	name     string
	summary  string
	usage    string
	flags    map[string]flagKind
	examples []string
	run      func(io.Writer, map[string]string, []string) int
}

type flagKind int

const (
	boolFlag flagKind = iota
	valueFlag
)

func (c command) helpLine() string {
	return "usage: " + c.usage
}

func (c command) help(w io.Writer) int {
	var d toon.Doc
	d.Field("command", "wt "+c.name)
	d.Field("summary", c.summary)
	d.Field("usage", c.usage)

	names := make([]string, 0, len(c.flags))
	for name := range c.flags {
		if name == "help" {
			continue
		}
		names = append(names, "--"+name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		d.Field("flags", strings.Join(names, " "))
	}
	if len(c.examples) > 0 {
		d.Help(c.examples...)
	}
	_, _ = d.WriteTo(w)
	return OK
}

// parse reads flags declared by the command and rejects anything else.
//
// Rejection carries the valid flags inline, so the agent's correction is one
// turn rather than a failed call followed by a --help call.
func parse(args []string, known map[string]flagKind) (map[string]string, []string, error) {
	flags := map[string]string{}
	var rest []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			rest = append(rest, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		value := ""
		if eq := strings.Index(name, "="); eq >= 0 {
			name, value = name[:eq], name[eq+1:]
		}

		if name == "help" || name == "h" {
			flags["help"] = "true"
			continue
		}

		kind, ok := known[name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown flag --%s; valid flags: %s", name, flagList(known))
		}
		if kind == boolFlag {
			flags[name] = "true"
			continue
		}
		if value == "" {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("--%s needs a value", name)
			}
			i++
			value = args[i]
		}
		flags[name] = value
	}
	return flags, rest, nil
}

func flagList(known map[string]flagKind) string {
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, "--"+name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

func commandNames() []string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// errorf writes a structured error to stdout, where the agent reads it, with a
// suggestion that names this tool's own commands.
func errorf(w io.Writer, code int, message string, help ...string) int {
	var d toon.Doc
	d.Field("error", message)
	d.Help(help...)
	_, _ = d.WriteTo(w)
	return code
}

func usage(w io.Writer) int {
	var d toon.Doc
	d.Field("bin", collapseHome(binPath()))
	d.Field("description", "A capped, machine-wide pool of git worktrees")
	d.Field("version", Version)

	rows := make([][]any, 0, len(commands))
	for _, name := range commandNames() {
		rows = append(rows, []any{name, commands[name].summary})
	}
	d.Table("commands", []string{"name", "summary"}, rows)
	d.Help(
		"Run `wt` to see the pool for the current repository",
		"Run `wt <command> --help` for one command",
	)
	_, _ = d.WriteTo(w)
	return OK
}

func binPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "wt"
	}
	return exe
}

func collapseHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || !strings.HasPrefix(path, home) {
		return path
	}
	return "~" + path[len(home):]
}
