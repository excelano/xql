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

// exitCodesBlock is the contract every xql usage block ends with. A caller
// that branches on the number rather than on the message needs to know that a
// query matching no rows is a success, not a failure: the empty result is the
// answer.
const exitCodesBlock = `Exit codes:
  0  success, including a query that matched nothing
  1  bad input -- unreadable source, a parse error, a rejected statement
  2  bad invocation -- unknown flag, missing argument, contradictory options`

// boolFlags reports which of fs's flags take no value, so an argument walk can
// tell `--exec SELECT ...` (flag plus value) from `--json FILE` (flag, then a
// positional).
func boolFlags(fs *flag.FlagSet) map[string]bool {
	out := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			out[f.Name] = true
		}
	})
	return out
}

// helpRequested reports whether args asks for help in flag position. Go's flag
// package treats -h/--help as a parse error, which would put an explicit help
// request on the failure exit; catching it here keeps it a success. Walking the
// arguments (rather than scanning for the string) means `--exec "--help"` is
// read as the statement it is.
func helpRequested(args []string, fs *flag.FlagSet) bool {
	isBool := boolFlags(fs)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if name == "h" || name == "help" {
			return true
		}
		if strings.ContainsRune(name, '=') {
			continue
		}
		if !isBool[name] && i+1 < len(args) {
			i++ // skip the flag's value so it is never mistaken for a flag
		}
	}
	return false
}
