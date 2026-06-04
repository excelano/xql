package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/excelano/xql/internal/parse"
	"github.com/excelano/xql/internal/sp"
)

// runSPImpl is the SharePoint-backend entry point. The dispatcher hands us
// argv stripped of "xql sp" — so args[0] is the first user-supplied token.
//
// Slice 1 wired --list. Slice 2 adds --exec / --format / --all-fields for
// one-shot SELECT queries. Writes (--commit, --confirm-destructive) and the
// REPL land in slices 3 and 4.
func runSPImpl(args []string) int {
	fs := flag.NewFlagSet("xql sp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		flagList      = fs.String("list", "", "SharePoint list URL (required)")
		flagExec      = fs.String("exec", "", "Run one SQL statement and exit (non-REPL mode)")
		flagFormat    = fs.String("format", "", "Output format: table | tsv | json (auto-detected if blank)")
		flagAllFields = fs.Bool("all-fields", false, "Include hidden/system fields in SELECT *")
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

	exec := &sp.Executor{
		Graph:     graph,
		Bound:     bound,
		Format:    *flagFormat,
		AllFields: *flagAllFields,
		Out:       os.Stdout,
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
		if err := exec.Execute(ctx, stmt, false); err != nil {
			fmt.Fprintf(os.Stderr, "Execution error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(os.Stderr, "Authenticated as: %s\n", result.Account.PreferredUsername)
	fmt.Fprintf(os.Stderr, "Connected to: %s (%d columns)\n", bound.DisplayName, len(bound.Columns))
	fmt.Fprintln(os.Stderr, "(REPL lands in slice 4; for now use --exec \"SELECT ...\" for one-shot queries)")
	return 0
}
