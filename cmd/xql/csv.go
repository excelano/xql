package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/excelano/xql/internal/cell"
	csvbackend "github.com/excelano/xql/internal/csv"
	"github.com/excelano/xql/internal/parse"
	"github.com/excelano/xql/internal/repl"
)

// runCSVImpl is the CSV-backend entry point. The dispatcher hands us argv
// stripped of "xql csv" — so args[0] is the first user-supplied token (either
// a flag or the file path).
//
// REPL support lands in slice 5; this slice wires --exec only and tells the
// user when they hit the file path with no statement.
func runCSVImpl(args []string) int {
	fs := flag.NewFlagSet("xql csv", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		flagExec           = fs.String("exec", "", "Run one SQL statement and exit (non-REPL mode)")
		flagMode           = fs.String("mode", "", "Output mode: table | tsv | csv | json (auto-detected if blank)")
		flagCommit         = fs.Bool("commit", false, "Commit writes in --exec mode (required for INSERT/UPDATE/DELETE)")
		flagConfirm        = fs.Bool("confirm-destructive", false, "Required for bare DELETE in --exec mode")
		flagOutput         = fs.String("output", "", "Write the statement result to this path as CSV (SELECT rows; or the modified table for committed UPDATE/DELETE/INSERT)")
		flagNoInputHeader  = fs.Bool("no-input-header", false, "Source CSV has no header row; columns are named col1, col2, ...")
		flagNoOutputHeader = fs.Bool("no-output-header", false, "Suppress the header row in output (table, tsv, csv modes)")
		flagDelim          = fs.String("delim", ",", "Single-character field delimiter (use \\t for tab)")
		flagTypes          = fs.String("type", "", "Comma-separated column type overrides, e.g. Priority=int,Tags=string")
	)

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: xql csv [flags] <csv-file>")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(reorderArgs(args, fs)); err != nil {
		return 2
	}

	csvPath := fs.Arg(0)
	if csvPath == "" {
		fmt.Fprintln(os.Stderr, "Error: CSV file path is required")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr, "Error: unexpected extra arguments after %q: %v\n", csvPath, fs.Args()[1:])
		return 2
	}

	delim, err := parseDelim(*flagDelim)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 2
	}

	hints, err := parseTypeHints(*flagTypes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 2
	}

	t, err := csvbackend.LoadCSV(csvPath, csvbackend.LoadOptions{
		Delim:     delim,
		NoHeader:  *flagNoInputHeader,
		TypeHints: hints,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load CSV: %v\n", err)
		return 1
	}

	exec := &csvbackend.Executor{
		Table:              t,
		Mode:               *flagMode,
		Headers:            !*flagNoOutputHeader,
		ConfirmDestructive: *flagConfirm,
		OutputPath:         *flagOutput,
		Out:                os.Stdout,
	}

	if *flagExec != "" {
		cleaned, bangCommit := parse.PreProcess(*flagExec)
		if bangCommit {
			fmt.Fprintln(os.Stderr, "Error: trailing '!' is not supported in --exec mode; use --commit")
			return 2
		}
		stmt, err := parse.Parse(cleaned)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
			return 1
		}
		if err := exec.Execute(stmt, *flagCommit); err != nil {
			fmt.Fprintf(os.Stderr, "Execution error: %v\n", err)
			return 1
		}
		return 0
	}

	session := &repl.Session{
		Out:         os.Stdout,
		Stderr:      os.Stderr,
		Prompt:      "xql> ",
		HistoryPath: filepath.Join(configDir(), "history-csv"),
		Banner: fmt.Sprintf(
			"Connected to: %s (%d columns, %d rows). Type \"help\" for commands, \"quit\" to exit.",
			t.Path, len(t.Columns), len(t.Rows),
		),
		Execute:    exec.Execute,
		Describe:   exec.Describe,
		Refresh:    exec.Refresh,
		SetConfirm: exec.SetConfirm,
	}
	if err := repl.Run(session); err != nil {
		fmt.Fprintf(os.Stderr, "REPL error: %v\n", err)
		return 1
	}
	return 0
}

// configDir returns ~/.config/xql, where REPL history lives. One directory
// shared across backends; history files distinguish by suffix (history-csv,
// history-sp).
func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "xql")
}

// reorderArgs moves positional arguments to the end so flag.FlagSet (which
// stops at the first non-flag token) can see flags wherever they appear. The
// set of boolean flags is discovered from fs itself so adding a new flag
// doesn't also require an edit here.
func reorderArgs(args []string, fs *flag.FlagSet) []string {
	boolFlag := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			boolFlag[f.Name] = true
		}
	})

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		// Look up the flag name (after stripping leading '-' or '--', up to '=').
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			continue
		}
		if !boolFlag[name] && i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, positional...)
}

// parseDelim accepts a single-character delimiter, with `\t` as a special
// case for tab.
func parseDelim(s string) (rune, error) {
	if s == `\t` || s == "\t" {
		return '\t', nil
	}
	runes := []rune(s)
	if len(runes) != 1 {
		return 0, fmt.Errorf("--delim must be one character (or \\t for tab), got %q", s)
	}
	return runes[0], nil
}

// parseTypeHints parses a "name=type,name=type" string into a map suitable
// for csvbackend.LoadOptions.TypeHints.
func parseTypeHints(s string) (map[string]cell.ColumnType, error) {
	if s == "" {
		return nil, nil
	}
	out := map[string]cell.ColumnType{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			return nil, fmt.Errorf("--type entry %q has no '=' (expected name=type)", pair)
		}
		name := strings.TrimSpace(pair[:eq])
		typeStr := strings.TrimSpace(pair[eq+1:])
		t, err := cell.ParseColumnType(typeStr)
		if err != nil {
			return nil, fmt.Errorf("--type %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}
