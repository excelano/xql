package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/excelano/spauth"
	"github.com/excelano/xql/internal/parse"
	"github.com/excelano/xql/internal/repl"
	"github.com/excelano/xql/internal/sp"
)

// runSPImpl is the SharePoint-backend entry point. The dispatcher hands us
// argv stripped of "xql sp" — so args[0] is the first user-supplied token
// (either a flag or the list URL).
func runSPImpl(args []string) int {
	fs := flag.NewFlagSet("xql sp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		flagExec           = fs.String("exec", "", "Run one SQL statement and exit (non-REPL mode)")
		flagDescribe       = fs.Bool("describe", false, "Print the bound list's column schema and exit; skip the REPL. Combine with --all-fields to include hidden columns")
		flagMode           = fs.String("mode", "", "Output mode: table | tsv | csv | json (auto-detected if blank)")
		flagJSON           = fs.Bool("json", false, jsonUsage)
		flagCommit         = fs.Bool("commit", false, "Commit writes in --exec mode (required for INSERT/UPDATE/DELETE)")
		flagAllFields      = fs.Bool("all-fields", false, "Include hidden/system fields in SELECT *")
		flagConfirm        = fs.Bool("confirm-destructive", false, "Required for bare DELETE (no WHERE) in --exec mode")
		flagOutput         = fs.String("output", "", "Write SELECT results as CSV to this path (SELECT only on sp)")
		flagNoOutputHeader = fs.Bool("no-output-header", false, "Suppress the header row in output (table, tsv, csv modes)")
	)

	usage := func(w io.Writer) {
		fmt.Fprintln(w, "Usage: xql sp [flags] <list-url>")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Flags:")
		printFlags(w, fs)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Authentication is device-code via Microsoft Graph; refresh tokens are cached at")
		fmt.Fprintln(w, spauth.CachePath()+", one session shared with the xfiles tools.")
		fmt.Fprintln(w)
		fmt.Fprintln(w, exitCodesBlock)
	}
	fs.Usage = func() { usage(os.Stderr) }

	if helpRequested(args, fs) {
		usage(os.Stdout)
		return 0
	}
	if err := fs.Parse(reorderArgs(args, fs)); err != nil {
		return 2
	}

	mode, err := resolveMode(*flagMode, *flagJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "xql: %v\n", err)
		return 2
	}

	listURL := fs.Arg(0)
	if listURL == "" {
		fmt.Fprintln(os.Stderr, "xql: a SharePoint list URL is required")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr, "xql: unexpected extra arguments after %q: %v\n", listURL, fs.Args()[1:])
		return 2
	}

	ctx := context.Background()
	// The cache xql kept before the family shared one; adopted on first run so
	// nobody signs in again.
	legacyTokenCache := filepath.Join(configDir(), "sp-token.json")

	client, err := spauth.NewPublicClient(spauth.CachePath(), legacyTokenCache)
	if err != nil {
		fmt.Fprintf(os.Stderr, "xql: setup: %v\n", err)
		return 1
	}

	result, err := spauth.Authenticate(ctx, client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "xql: authentication failed: %v%s\n", err, spauth.HintForAuthError(err))
		return 1
	}

	// The Prefer header is required for ad-hoc $filter / $orderby on
	// SharePoint list-item fields; without it any fields/<col> filter returns
	// 400 invalidRequest. The timeout is per request, and a list page is a
	// small JSON document.
	graph := spauth.NewGraphClient(client, result.Account,
		spauth.WithTimeout(60*time.Second),
		spauth.WithHeader("Prefer", "HonorNonIndexedQueriesWarningMayFailRandomly"),
	)

	bound, err := sp.ResolveListBinding(ctx, graph, listURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "xql: could not bind list: %v\n", err)
		return 1
	}

	if *flagOutput != "" {
		if err := repl.TruncateOutputFile(*flagOutput); err != nil {
			fmt.Fprintf(os.Stderr, "xql: %v\n", err)
			return 1
		}
	}

	exec := &sp.Executor{
		Graph:              graph,
		Bound:              bound,
		Mode:               mode,
		Headers:            !*flagNoOutputHeader,
		AllFields:          *flagAllFields,
		ConfirmDestructive: *flagConfirm,
		OutputPath:         *flagOutput,
		Out:                os.Stdout,
	}

	if *flagDescribe {
		arg := ""
		if *flagAllFields {
			arg = "all"
		}
		if err := exec.Describe(os.Stdout, arg); err != nil {
			fmt.Fprintf(os.Stderr, "xql: describe: %v\n", err)
			return 1
		}
		return 0
	}

	if *flagExec != "" {
		cleaned, bangCommit := parse.PreProcess(*flagExec)
		if bangCommit {
			fmt.Fprintln(os.Stderr, "xql: trailing '!' is not supported in --exec mode; use --commit")
			return 2
		}
		stmt, err := parse.Parse(cleaned)
		if err != nil {
			fmt.Fprintf(os.Stderr, "xql: %v\n", err)
			return 1
		}
		if err := exec.Execute(ctx, stmt, *flagCommit); err != nil {
			fmt.Fprintf(os.Stderr, "xql: execute: %v\n", err)
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
		SetAllFields:  func(on bool) { exec.AllFields = on },
		GetAllFields:  func() bool { return exec.AllFields },
	}
	if err := repl.Run(session); err != nil {
		fmt.Fprintf(os.Stderr, "xql: repl: %v\n", err)
		return 1
	}
	return 0
}
