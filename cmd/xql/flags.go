package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// printFlags formats fs's flags for --help output with a double-dash prefix,
// matching the README's conventions and the wider tools ecosystem
// (git, gh, cargo, docker). Go's flag package parses one- or two-dash forms
// identically, so both `xql csv -exec` and `xql csv --exec` still work; only
// the printed help changes. Layout mirrors stdlib PrintDefaults so column
// alignment stays consistent with the rest of the help block.
func printFlags(w io.Writer, fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		var b strings.Builder
		fmt.Fprintf(&b, "  --%s", f.Name)
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
