package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/excelano/xql/internal/csv"
	"github.com/excelano/xql/internal/parse"
	"github.com/excelano/xql/internal/repl"
	"github.com/excelano/xql/internal/xinglet"
)

// runXingletImpl is the Xinglet-backend entry point. The dispatcher hands us
// argv stripped of "xql xinglet" -- so args[0] is the first user-supplied
// token (either a flag or the xinglet:// URL).
//
// The backend is read-only: --commit and --confirm-destructive don't appear
// because INSERT/UPDATE/DELETE are rejected before the parser is called.
func runXingletImpl(args []string) int {
	fs := flag.NewFlagSet("xql xinglet", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		flagExec           = fs.String("exec", "", "Run one SQL statement and exit (non-REPL mode)")
		flagDescribe       = fs.Bool("describe", false, "Print the loaded xinglet's column schema and exit; skip the REPL")
		flagMode           = fs.String("mode", "", "Output mode: table | tsv | csv | json (auto-detected if blank)")
		flagOutput         = fs.String("output", "", "Write SELECT results as CSV to this path")
		flagNoOutputHeader = fs.Bool("no-output-header", false, "Suppress the header row in output (table, tsv, csv modes)")
	)

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: xql xinglet [flags] xinglet://<uuid>")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		printFlags(os.Stderr, fs)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Environment:")
		fmt.Fprintln(os.Stderr, "  XINGLET_TOKEN     Bearer token (required). Mint at <base>/home/tokens.php.")
		fmt.Fprintln(os.Stderr, "  XINGLET_BASE_URL  Server base URL (default https://xinglet.com).")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "The xinglet backend is read-only; INSERT/UPDATE/DELETE are rejected.")
	}

	if err := fs.Parse(reorderArgs(args, fs)); err != nil {
		return 2
	}

	rawURL := fs.Arg(0)
	if rawURL == "" {
		fmt.Fprintln(os.Stderr, "Error: xinglet:// URL is required")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr, "Error: unexpected extra arguments after %q: %v\n", rawURL, fs.Args()[1:])
		return 2
	}

	uuid, err := xinglet.ParseURL(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 2
	}

	token := os.Getenv("XINGLET_TOKEN")
	if token == "" {
		base := os.Getenv("XINGLET_BASE_URL")
		if base == "" {
			base = xinglet.DefaultBaseURL
		}
		fmt.Fprintf(os.Stderr, "Error: XINGLET_TOKEN is not set (mint one at %s/home/tokens.php).\n", base)
		return 2
	}

	cfg := xinglet.Config{
		BaseURL:   os.Getenv("XINGLET_BASE_URL"),
		Token:     token,
		UserAgent: "xql/" + version,
	}

	table, err := xinglet.LoadList(cfg, uuid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load xinglet: %v\n", err)
		return 1
	}

	if *flagOutput != "" {
		if err := repl.TruncateOutputFile(*flagOutput); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
	}

	inner := &csv.Executor{
		Table:      table,
		Mode:       *flagMode,
		Headers:    !*flagNoOutputHeader,
		OutputPath: *flagOutput,
		Out:        os.Stdout,
	}
	exec := &xinglet.Executor{Inner: inner, Cfg: cfg, UUID: uuid}

	if *flagDescribe {
		if err := exec.Describe(os.Stdout, ""); err != nil {
			fmt.Fprintf(os.Stderr, "describe error: %v\n", err)
			return 1
		}
		return 0
	}

	if *flagExec != "" {
		cleaned, bangCommit := parse.PreProcess(*flagExec)
		if bangCommit {
			fmt.Fprintln(os.Stderr, "Error: trailing '!' is not supported in --exec mode (and the xinglet backend is read-only anyway)")
			return 2
		}
		stmt, err := parse.Parse(cleaned)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
			return 1
		}
		if err := exec.Execute(stmt, false); err != nil {
			fmt.Fprintf(os.Stderr, "Execution error: %v\n", err)
			return 1
		}
		return 0
	}

	session := &repl.Session{
		Out:         os.Stdout,
		Stderr:      os.Stderr,
		Prompt:      "xql> ",
		HistoryPath: filepath.Join(configDir(), "history-xinglet"),
		Banner: fmt.Sprintf(
			"Connected to: %s (%d columns, %d rows). Type \"help\" for commands, \"quit\" to exit.",
			table.Path, len(table.Columns), len(table.Rows),
		),
		Execute:       exec.Execute,
		Describe:      exec.Describe,
		Refresh:       exec.Refresh,
		SetConfirm:    exec.SetConfirm,
		SetMode:       func(m string) { inner.Mode = m },
		SetHeaders:    func(on bool) { inner.Headers = on },
		SetOutputPath: func(p string) { inner.OutputPath = p },
	}
	if err := repl.Run(session); err != nil {
		fmt.Fprintf(os.Stderr, "REPL error: %v\n", err)
		return 1
	}
	return 0
}
