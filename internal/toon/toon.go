// Package toon writes the output format agents read.
//
// It lives at the output boundary on purpose: everything above works in Go
// values, and only this package knows what the wire looks like. TOON costs
// roughly forty per cent fewer tokens than the equivalent JSON, which matters
// when a tool is called on every session start.
package toon

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Doc is one response, written in the order fields were added.
type Doc struct {
	parts []string
}

// Field writes a single scalar as `key: value`.
func (d *Doc) Field(key string, value any) *Doc {
	d.parts = append(d.parts, fmt.Sprintf("%s: %s", key, scalar(value)))
	return d
}

// Section writes a nested block of scalars under one key.
func (d *Doc) Section(key string, fields map[string]any) *Doc {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	fmt.Fprintf(&b, "%s:", key)
	for _, k := range keys {
		fmt.Fprintf(&b, "\n  %s: %s", k, scalar(fields[k]))
	}
	d.parts = append(d.parts, b.String())
	return d
}

// Table writes a uniform array: a header naming the columns and the row count,
// then one comma-separated line per row.
//
// The count is in the header because an agent that cannot see how many rows
// exist will call again with a larger limit to find out.
func (d *Doc) Table(key string, columns []string, rows [][]any) *Doc {
	var b strings.Builder
	fmt.Fprintf(&b, "%s[%d]{%s}:", key, len(rows), strings.Join(columns, ","))
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = scalar(cell)
		}
		fmt.Fprintf(&b, "\n  %s", strings.Join(cells, ","))
	}
	d.parts = append(d.parts, b.String())
	return d
}

// Help writes the next steps an agent can take from here.
func (d *Doc) Help(lines ...string) *Doc {
	if len(lines) == 0 {
		return d
	}
	var b strings.Builder
	fmt.Fprintf(&b, "help[%d]:", len(lines))
	for _, line := range lines {
		fmt.Fprintf(&b, "\n  %s", line)
	}
	d.parts = append(d.parts, b.String())
	return d
}

// WriteTo renders the document.
func (d *Doc) WriteTo(w io.Writer) (int64, error) {
	n, err := fmt.Fprintln(w, strings.Join(d.parts, "\n"))
	return int64(n), err
}

// String renders the document, for tests.
func (d *Doc) String() string { return strings.Join(d.parts, "\n") + "\n" }

// scalar quotes only what would otherwise be misread: a value carrying a comma,
// a colon, a quote or a newline, and the empty string. Everything else is bare,
// which is where the token saving comes from.
func scalar(v any) string {
	s := fmt.Sprint(v)
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ",:\"\n") {
		return `"` + strings.NewReplacer(`"`, `\"`, "\n", `\n`).Replace(s) + `"`
	}
	return s
}
