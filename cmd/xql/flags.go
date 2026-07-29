package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// printFlags formats fs's flags for --help output with a double-dash prefix,
// matching the README's conventions and the wider tools ecosystem
// (git, gh, cargo, docker). Go's flag package parses one- or two-dash forms
// identically, so both `xql csv -exec` and `xql csv --exec` still work; only
// the printed help changes. Layout mirrors stdlib PrintDefaults so column
// alignment stays consistent with the rest of the help block.
//
// A short alias (`-d` for `--delim`) is registered as a second flag bound to
// the same variable, which makes the two share one Value pointer. Grouping on
// that pointer lets both spellings print on one line — `-d, --delim` — instead
// of appearing as two unrelated options.
func printFlags(w io.Writer, fs *flag.FlagSet) {
	spellings := map[flag.Value][]string{}
	fs.VisitAll(func(f *flag.Flag) {
		spellings[f.Value] = append(spellings[f.Value], f.Name)
	})

	fs.VisitAll(func(f *flag.Flag) {
		names := append([]string(nil), spellings[f.Value]...)
		sort.SliceStable(names, func(i, j int) bool { return len(names[i]) < len(names[j]) })
		if f.Name != names[len(names)-1] {
			return // an alias; the longest spelling prints the group's line
		}
		var b strings.Builder
		b.WriteString("  ")
		for i, n := range names {
			if i > 0 {
				b.WriteString(", ")
			}
			if len(n) == 1 {
				fmt.Fprintf(&b, "-%s", n)
			} else {
				fmt.Fprintf(&b, "--%s", n)
			}
		}
		name, usage := flag.UnquoteUsage(f)
		if name != "" {
			b.WriteString(" ")
			b.WriteString(name)
		}
		if b.Len() <= 5 {
			b.WriteString("\t")
		} else {
			b.WriteString("\n    \t")
		}
		b.WriteString(strings.ReplaceAll(usage, "\n", "\n    \t"))
		if !isZeroDefaultValue(f) {
			fmt.Fprintf(&b, " (default %q)", f.DefValue)
		}
		fmt.Fprintln(w, b.String())
	})
}

// jsonUsage describes the --json shorthand. Every backend registers the same
// flag, so the wording lives in one place.
const jsonUsage = "Emit JSON; shorthand for --mode json"

// resolveMode folds the --json shorthand into --mode. xray spells JSON output
// --json, so reaching for that spelling here finds it too, while --mode keeps
// the other three formats addressable. Giving both is fine when they agree and
// an error when they don't — silently letting one win would make a mistyped
// pipeline look like it worked.
func resolveMode(mode string, jsonShorthand bool) (string, error) {
	if !jsonShorthand {
		return mode, nil
	}
	if mode != "" && !strings.EqualFold(mode, "json") {
		return "", fmt.Errorf("--json conflicts with --mode %s; give one or the other", mode)
	}
	return "json", nil
}

// isZeroDefaultValue suppresses the "(default ...)" trailer for flags that
// carry a zero-valued default. Booleans default to false and unset strings
// to the empty string; showing that trailer would clutter the help.
func isZeroDefaultValue(f *flag.Flag) bool {
	switch f.DefValue {
	case "", "false", "0":
		return true
	}
	return false
}
