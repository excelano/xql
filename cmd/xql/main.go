// Command xql is the Excelano Query Language CLI: one binary, one language,
// many backends. Backends register a name, optional file-extension list, and
// a Run function; the dispatcher routes argv[1:] to whichever backend matches.
//
// Dispatch order (see project-xql memory for rationale):
//  1. argv[1] matches a registered subcommand name -> Run(argv[2:]).
//  2. argv[1] has a recognized file extension      -> Run(argv[1:]).
//  3. Else -> usage error.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Backend is the registration record for an XQL backend.
type Backend struct {
	Name       string
	Extensions []string // lowercase, dot-prefixed (e.g. ".csv"); nil disables extension inference.
	Summary    string
	Run        func(args []string) int
}

var backends = []Backend{
	{
		Name:       "csv",
		Extensions: []string{".csv", ".tsv"},
		Summary:    "Run XQL against a local CSV (or TSV) file.",
		Run:        runCSV,
	},
	{
		Name:       "sp",
		Extensions: nil, // never inferred: URLs are polymorphic and auth is required.
		Summary:    "Run XQL against a SharePoint list (auth required).",
		Run:        runSP,
	},
}

func main() {
	os.Exit(dispatch(os.Args[1:], backends, os.Stdout, os.Stderr))
}

func dispatch(args []string, reg []Backend, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr, reg)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		printUsage(stdout, reg)
		return 0
	case "-V", "--version":
		fmt.Fprintln(stdout, version)
		return 0
	}

	for _, b := range reg {
		if args[0] == b.Name {
			return b.Run(args[1:])
		}
	}

	ext := strings.ToLower(filepath.Ext(args[0]))
	if ext != "" {
		for _, b := range reg {
			for _, candidate := range b.Extensions {
				if ext == candidate {
					return b.Run(args)
				}
			}
		}
		fmt.Fprintf(stderr, "xql: no backend handles files with extension %q.\n", ext)
		fmt.Fprintln(stderr, "Use an explicit subcommand, e.g. xql csv FILE.")
		return 2
	}

	fmt.Fprintf(stderr, "xql: unknown subcommand %q (and no recognized file extension).\n", args[0])
	printUsage(stderr, reg)
	return 2
}

// Stamped at build time via -ldflags by goreleaser.
var version = "(devel)"

func printUsage(w io.Writer, reg []Backend) {
	fmt.Fprintln(w, "xql — Excelano Query Language CLI")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  xql <backend> [backend-args...]")
	fmt.Fprintln(w, "  xql <file>    [backend-args...]   (backend inferred from extension)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Backends:")
	for _, b := range reg {
		exts := "(no extension inference)"
		if len(b.Extensions) > 0 {
			exts = "inferred from " + strings.Join(b.Extensions, ", ")
		}
		fmt.Fprintf(w, "  %-4s  %s\n        %s\n", b.Name, b.Summary, exts)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Backend help:")
	fmt.Fprintln(w, "  xql csv --help")
	fmt.Fprintln(w, "  xql sp  --help")
}

// runCSV delegates to runCSVImpl in csv.go and runSP delegates to runSPImpl
// in sp.go. Thin shims so the Backend table's function values stay stable
// identifiers (the backend bodies live with the rest of their flag parsing).
func runCSV(args []string) int { return runCSVImpl(args) }
func runSP(args []string) int  { return runSPImpl(args) }
