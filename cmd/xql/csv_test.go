package main

import (
	"bytes"
	"flag"
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
