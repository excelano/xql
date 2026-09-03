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

	"github.com/excelano/xql"
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
	{
		Name:       "xinglet",
		Extensions: nil, // xinglet:// is a URL form, not a file extension.
		Summary:    "Run XQL against a remote xinglist (Bearer token required, read-only).",
		Run:        runXinglet,
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
		fmt.Fprintf(stdout, "xql %s\n", resolveVersion())
		return 0
	// Terminal actions: they touch the user's skills directory and nothing
	// else, so no backend is bound and no credential is read on the way through.
	case "--install-skill":
		return xql.InstallSkill(resolveVersion())
	case "--uninstall-skill":
		return xql.UninstallSkill()
	// The bare state command binds no list and reads the token cache only,
	// so it too is answered before any backend is chosen.
	case "auth":
		return runAuth(args[1:])
	}

	// Find the first non-flag token to route on, so `xql --describe data.csv`
	// works the same as `xql data.csv --describe`. Leading `-flag` tokens
	// (other than -h/--help/-V/--version, already handled above) get skipped
	// here; each backend's flag parser handles the eventual reordering via
	// reorderArgs, so the leading flag still binds correctly downstream. A lone
	// `-` is the family's spelling for stdin, so it routes rather than skips.
	routeIdx := 0
	for routeIdx < len(args) && strings.HasPrefix(args[routeIdx], "-") && args[routeIdx] != "-" {
		routeIdx++
	}
	if routeIdx >= len(args) {
		fmt.Fprintf(stderr, "xql: no backend or file given — every argument was a flag (%s).\n",
			strings.Join(quoteAll(args), ", "))
		fmt.Fprintln(stderr, "A flag alone does not select a backend: write `xql csv FILE ...` or `xql FILE ...`.")
		// Only offer a correction for a near-miss on a global flag. Most flags
		// that land here are real backend flags given without a backend, and
		// telling their owner they do not exist would be wrong.
		if near := nearestGlobal(args[0]); near != "" {
			fmt.Fprintf(stderr, "If %q was meant as a global flag, did you mean %s?\n", args[0], near)
		}
		return 2
	}
	route := args[routeIdx]

	// stdin carries no extension, so there is nothing to infer a backend from.
	if route == "-" {
		fmt.Fprintln(stderr, "xql: `-` reads stdin, but stdin has no extension to infer a backend from.")
		fmt.Fprintln(stderr, "Name the backend: `xql csv - --exec \"SELECT ...\"`.")
		return 2
	}

	for _, b := range reg {
		if route == b.Name {
			// Strip the subcommand name from wherever it appears; preserve
			// leading flags so they still reach the backend's parser.
			passthrough := make([]string, 0, len(args)-1)
			passthrough = append(passthrough, args[:routeIdx]...)
			passthrough = append(passthrough, args[routeIdx+1:]...)
			return b.Run(passthrough)
		}
	}

	ext := strings.ToLower(filepath.Ext(route))
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

	fmt.Fprintf(stderr, "xql: unknown subcommand %q (and no recognized file extension).\n", route)
	printUsage(stderr, reg)
	return 2
}

// globalFlags are the flags dispatch handles itself, before any backend is
// bound. They are the only candidates for a did-you-mean on an argument list
// that turned out to be nothing but flags.
var globalFlags = []string{"--help", "--version", "--install-skill", "--uninstall-skill"}

// nearestGlobal names the global flag closest to arg, or "" if none is close.
// The bar is two edits, which catches the realistic typo — a transposition
// (`--verison`) or a dropped letter (`--instal-skill`) — without inventing a
// suggestion for a flag that legitimately belongs to a backend.
func nearestGlobal(arg string) string {
	want := strings.TrimLeft(arg, "-")
	best, bestDist := "", 3
	for _, g := range globalFlags {
		if d := editDistance(want, strings.TrimLeft(g, "-")); d < bestDist {
			best, bestDist = g, d
		}
	}
	return best
}

// editDistance is Levenshtein over bytes. Flag names are ASCII, so byte
// distance and rune distance are the same thing here.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = fmt.Sprintf("%q", a)
	}
	return out
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
		fmt.Fprintf(w, "  %-8s  %s\n            %s\n", b.Name, b.Summary, exts)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Backend help:")
	fmt.Fprintln(w, "  xql csv     --help")
	fmt.Fprintln(w, "  xql sp      --help")
	fmt.Fprintln(w, "  xql xinglet --help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Session:")
	fmt.Fprintln(w, "  xql auth [--json]    report the SharePoint sign-in state shared with the xfiles tools")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Claude Code:")
	fmt.Fprintln(w, "  --install-skill      install the xql skill into ~/.claude/skills/xql")
	fmt.Fprintln(w, "  --uninstall-skill    remove it again")
	fmt.Fprintln(w)
	fmt.Fprintln(w, exitCodesBlock)
}

// runCSV, runSP, and runXinglet are thin shims so the Backend table's
// function values stay stable identifiers (the backend bodies live with
// the rest of their flag parsing in csv.go / sp.go / xinglet.go).
func runCSV(args []string) int     { return runCSVImpl(args) }
func runSP(args []string) int      { return runSPImpl(args) }
func runXinglet(args []string) int { return runXingletImpl(args) }
