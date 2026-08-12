package main

import (
	"bytes"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

func TestResolveDelim(t *testing.T) {
	cases := []struct {
		name  string
		flag  string
		path  string
		want  rune
		isErr bool
	}{
		{name: "no flag on a .csv is comma", path: "data.csv", want: ','},
		{name: "no flag on a .tsv is tab", path: "data.tsv", want: '\t'},
		{name: "extension match is case-insensitive", path: "DATA.TSV", want: '\t'},
		{name: "no flag on an unknown extension is comma", path: "data.txt", want: ','},
		{name: "no flag and no extension is comma", path: "data", want: ','},
		{name: "explicit flag beats the .tsv extension", flag: ",", path: "data.tsv", want: ','},
		{name: "explicit tab escape on a .txt", flag: `\t`, path: "data.txt", want: '\t'},
		{name: "explicit pipe", flag: "|", path: "data.csv", want: '|'},
		{name: "multi-character flag is an error", flag: "ab", path: "data.csv", isErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDelim(tc.flag, tc.path)
			if tc.isErr {
				if err == nil {
					t.Fatalf("resolveDelim(%q, %q) = %q, want an error", tc.flag, tc.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveDelim(%q, %q) returned %v", tc.flag, tc.path, err)
			}
			if got != tc.want {
				t.Errorf("resolveDelim(%q, %q) = %q, want %q", tc.flag, tc.path, got, tc.want)
			}
		})
	}
}

// A short alias binds the same variable as its long spelling, so either form
// parses and the help block lists them together on one line.
func TestDelimAliasParsesAndPrintsOnce(t *testing.T) {
	newFS := func(target *string) *flag.FlagSet {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(&bytes.Buffer{})
		fs.StringVar(target, "delim", "", "Field delimiter")
		fs.StringVar(target, "d", "", "Field delimiter")
		return fs
	}

	for _, args := range [][]string{{"-d", "|"}, {"--delim", "|"}, {"-d=|"}, {"--delim=|"}} {
		var delim string
		if err := newFS(&delim).Parse(args); err != nil {
			t.Fatalf("parsing %v: %v", args, err)
		}
		if delim != "|" {
			t.Errorf("parsing %v gave delim %q, want %q", args, delim, "|")
		}
	}

	var delim string
	var out bytes.Buffer
	printFlags(&out, newFS(&delim))
	got := out.String()
	if want := "  -d, --delim string\n"; !strings.Contains(got, want) {
		t.Errorf("help block missing %q; got:\n%s", want, got)
	}
	if n := strings.Count(got, "Field delimiter"); n != 1 {
		t.Errorf("help block describes the delimiter %d times, want 1; got:\n%s", n, got)
	}
}

// The family spelling --no-header reaches the same switch as the CSV backend's
// own --no-input-header, and a bool alias stays a bool through reorderArgs (so
// it never swallows the file path that follows it).
func TestNoHeaderAliasIsBoolAndBindsTheSameVariable(t *testing.T) {
	newFS := func(target *bool) *flag.FlagSet {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(&bytes.Buffer{})
		fs.BoolVar(target, "no-input-header", false, "Source has no header row")
		fs.BoolVar(target, "no-header", false, "Source has no header row")
		return fs
	}

	for _, args := range [][]string{{"--no-header"}, {"--no-input-header"}, {"-no-header"}} {
		var noHeader bool
		if err := newFS(&noHeader).Parse(args); err != nil {
			t.Fatalf("parsing %v: %v", args, err)
		}
		if !noHeader {
			t.Errorf("parsing %v left the header flag unset", args)
		}
	}

	var noHeader bool
	fs := newFS(&noHeader)
	got := reorderArgs([]string{"--no-header", "data.csv"}, fs)
	if len(got) != 2 || got[0] != "--no-header" || got[1] != "data.csv" {
		t.Errorf("reorderArgs treated --no-header as taking a value: %v", got)
	}

	var out bytes.Buffer
	printFlags(&out, newFS(&noHeader))
	if want := "  --no-header, --no-input-header\n"; !strings.Contains(out.String(), want) {
		t.Errorf("help block missing %q; got:\n%s", want, out.String())
	}
}

// withStdin runs fn with os.Stdin replaced by a pipe carrying input, so the
// `-` path can be exercised end to end.
func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = saved; r.Close() }()
	go func() {
		_, _ = io.WriteString(w, input)
		w.Close()
	}()
	fn()
}

// quiet swallows whatever the backend writes while fn runs, so a test that is
// only asserting on an exit code doesn't scribble over the test log.
func quiet(t *testing.T, fn func()) {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("devnull: %v", err)
	}
	defer devnull.Close()
	outSaved, errSaved := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = devnull, devnull
	defer func() { os.Stdout, os.Stderr = outSaved, errSaved }()
	fn()
}

func TestDashReadsTheTableFromStdin(t *testing.T) {
	const table = "id,name,qty\n007,Ann,5\n008,Bob,3\n"

	t.Run("a statement over stdin succeeds", func(t *testing.T) {
		var code int
		withStdin(t, table, func() {
			quiet(t, func() { code = runCSVImpl([]string{"-", "--exec", "SELECT *"}) })
		})
		if code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
	})

	// Without --exec or --describe the backend would open a REPL on the same
	// stream the table just came from, so it is refused rather than hung on.
	t.Run("stdin with no statement is refused", func(t *testing.T) {
		var code int
		quiet(t, func() { code = runCSVImpl([]string{"-"}) })
		if code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
	})

	// There is no file behind stdin to write a committed change back to.
	t.Run("committing from stdin needs an output path", func(t *testing.T) {
		var code int
		quiet(t, func() {
			code = runCSVImpl([]string{"-", "--exec", `UPDATE SET name = "Z"`, "--commit"})
		})
		if code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
	})
}

func TestBackendHelpIsSuccess(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		var code int
		quiet(t, func() { code = runCSVImpl(args) })
		if code != 0 {
			t.Errorf("runCSVImpl(%q) = %d, want 0", args, code)
		}
	}
}
