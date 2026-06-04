package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/excelano/xql/internal/parse"
	"github.com/excelano/xql/internal/repl"
	"github.com/excelano/xql/internal/sp"
)

// runSPImpl is the SharePoint-backend entry point. The dispatcher hands us
// argv stripped of "xql sp" — so args[0] is the first user-supplied token.
//
// Slice 1 wired --list. Slice 2 added --exec / --format / --all-fields for
// one-shot SELECT. Slice 3 added --commit / --confirm-destructive for
// UPDATE/DELETE/INSERT with dry-run preview. Slice 4 wires the interactive
// REPL when --exec is absent.
func runSPImpl(args []string) int {
	fs := flag.NewFlagSet("xql sp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		flagList           = fs.String("list", "", "SharePoint list URL (required)")
		flagExec           = fs.String("exec", "", "Run one SQL statement and exit (non-REPL mode)")
		flagMode           = fs.String("mode", "", "Output mode: table | tsv | csv | json (auto-detected if blank)")
		flagCommit         = fs.Bool("commit", false, "Commit writes in --exec mode (required for INSERT/UPDATE/DELETE)")
		flagAllFields      = fs.Bool("all-fields", false, "Include hidden/system fields in SELECT *")
		flagConfirm        = fs.Bool("confirm-destructive", false, "Required for bare DELETE (no WHERE) in --exec mode")
		flagOutput         = fs.String("output", "", "Write SELECT results as CSV to this path (SELECT only on sp)")
		flagNoOutputHeader = fs.Bool("no-output-header", false, "Suppress the header row in output (table, tsv, csv modes)")
	)

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: xql sp --list <list-url> [--exec STATEMENT] [flags]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Authentication is device-code via Microsoft Graph; refresh tokens are cached at")
		fmt.Fprintln(os.Stderr, "~/.config/xql/sp-token.json.")
	}

	if err := fs.Parse(reorderArgs(args, fs)); err != nil {
		return 2
	}

	if *flagList == "" {
		fmt.Fprintln(os.Stderr, "Error: --list is required")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "Error: unexpected positional arguments: %v\n", fs.Args())
		return 2
	}

	ctx := context.Background()
	tokenCachePath := filepath.Join(configDir(), "sp-token.json")

	client, err := sp.NewPublicClient(tokenCachePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Setup error: %v\n", err)
		return 1
	}

	result, err := sp.Authenticate(ctx, client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Authentication failed: %v%s\n", err, sp.HintForAuthError(err))
		return 1
	}

	graph := sp.NewGraphClient(client, result.Account)

	bound, err := sp.ResolveListBinding(ctx, graph, *flagList)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to bind list: %v\n", err)
		return 1
	}

	if *flagOutput != "" {
		if err := repl.TruncateOutputFile(*flagOutput); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
	}

	exec := &sp.Executor{
		Graph:              graph,
		Bound:              bound,
		Mode:               *flagMode,
		Headers:            !*flagNoOutputHeader,
		AllFields:          *flagAllFields,
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
		if err := exec.Execute(ctx, stmt, *flagCommit); err != nil {
			fmt.Fprintf(os.Stderr, "Execution error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(os.Stderr, "Authenticated as: %s\n", result.Account.PreferredUsername)

	session := &repl.Session{
		Out:         os.Stdout,
		Stderr:      os.Stderr,
		Prompt:      "xql> ",
		HistoryPath: filepath.Join(configDir(), "history-sp"),
		Banner: fmt.Sprintf(
			"Connected to: %s (%d columns). Type \"help\" for commands, \"quit\" to exit.",
			bound.DisplayName, len(bound.Columns),
		),
		Execute: func(stmt parse.Stmt, commit bool) error {
			return exec.Execute(ctx, stmt, commit)
		},
		Describe:      exec.Describe,
		Refresh:       exec.Refresh,
		SetConfirm:    exec.SetConfirm,
		SetMode:       func(m string) { exec.Mode = m },
		SetHeaders:    func(on bool) { exec.Headers = on },
		SetOutputPath: func(p string) { exec.OutputPath = p },
	}
	if err := repl.Run(session); err != nil {
		fmt.Fprintf(os.Stderr, "REPL error: %v\n", err)
		return 1
	}
	return 0
}
