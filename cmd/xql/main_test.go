package main

import (
	"bytes"
	"strings"
	"testing"
)

// recorder is a backend.Run that records the args it was called with.
type recorder struct {
	calls [][]string
	code  int
}

func (r *recorder) run(args []string) int {
	cp := make([]string, len(args))
	copy(cp, args)
	r.calls = append(r.calls, cp)
	return r.code
}

func TestDispatch(t *testing.T) {
	csvRec := &recorder{code: 0}
	spRec := &recorder{code: 0}
	reg := []Backend{
		{Name: "csv", Extensions: []string{".csv", ".tsv"}, Summary: "csv backend", Run: csvRec.run},
		{Name: "sp", Extensions: nil, Summary: "sp backend", Run: spRec.run},
	}

	type want struct {
		code         int
		csvCalls     [][]string
		spCalls      [][]string
		stdoutSubstr string
		stderrSubstr string
	}
	cases := []struct {
		name string
		args []string
		want want
	}{
		{
			name: "no args prints usage to stderr and exits 2",
			args: nil,
			want: want{code: 2, stderrSubstr: "Backends:"},
		},
		{
			name: "--help prints usage to stdout and exits 0",
			args: []string{"--help"},
			want: want{code: 0, stdoutSubstr: "Backends:"},
		},
		{
			name: "-h prints usage to stdout and exits 0",
			args: []string{"-h"},
			want: want{code: 0, stdoutSubstr: "Backends:"},
		},
		{
			name: "bare help word prints usage to stdout and exits 0",
			args: []string{"help"},
			want: want{code: 0, stdoutSubstr: "Backends:"},
		},
		{
			name: "subcommand name routes with empty args",
			args: []string{"csv"},
			want: want{code: 0, csvCalls: [][]string{{}}},
		},
		{
			name: "subcommand name strips itself from args",
			args: []string{"csv", "data.csv", "--exec", "select 1"},
			want: want{code: 0, csvCalls: [][]string{{"data.csv", "--exec", "select 1"}}},
		},
		{
			name: "sp routes with empty extension list (never inferred)",
			args: []string{"sp", "--site", "x", "--list", "y"},
			want: want{code: 0, spCalls: [][]string{{"--site", "x", "--list", "y"}}},
		},
		{
			name: "extension inference keeps the file in argv",
			args: []string{"data.csv"},
			want: want{code: 0, csvCalls: [][]string{{"data.csv"}}},
		},
		{
			name: "extension inference passes downstream flags through",
			args: []string{"data.csv", "--exec", "select 1"},
			want: want{code: 0, csvCalls: [][]string{{"data.csv", "--exec", "select 1"}}},
		},
		{
			name: "tsv extension also routes to csv backend",
			args: []string{"weird.TSV"},
			want: want{code: 0, csvCalls: [][]string{{"weird.TSV"}}},
		},
		{
			name: "literal filename equals subcommand: subcommand wins",
			args: []string{"csv"},
			want: want{code: 0, csvCalls: [][]string{{}}},
		},
		{
			name: "filename of literal csv via explicit subcommand passes through",
			args: []string{"csv", "./csv"},
			want: want{code: 0, csvCalls: [][]string{{"./csv"}}},
		},
		{
			name: "no extension is an error (no content sniffing)",
			args: []string{"data"},
			want: want{code: 2, stderrSubstr: "unknown subcommand"},
		},
		{
			name: "wrong extension is an error (no content sniffing)",
			args: []string{"data.txt"},
			want: want{code: 2, stderrSubstr: "no backend handles files with extension"},
		},
		{
			name: "unknown extension reports the extension",
			args: []string{"foo.json"},
			want: want{code: 2, stderrSubstr: `".json"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csvRec.calls = nil
			spRec.calls = nil
			var stdout, stderr bytes.Buffer

			got := dispatch(tc.args, reg, &stdout, &stderr)

			if got != tc.want.code {
				t.Errorf("exit code = %d, want %d (stdout=%q stderr=%q)", got, tc.want.code, stdout.String(), stderr.String())
			}
			if tc.want.stdoutSubstr != "" && !strings.Contains(stdout.String(), tc.want.stdoutSubstr) {
				t.Errorf("stdout missing %q; got %q", tc.want.stdoutSubstr, stdout.String())
			}
			if tc.want.stderrSubstr != "" && !strings.Contains(stderr.String(), tc.want.stderrSubstr) {
				t.Errorf("stderr missing %q; got %q", tc.want.stderrSubstr, stderr.String())
			}
			if !equalCalls(csvRec.calls, tc.want.csvCalls) {
				t.Errorf("csv calls = %v, want %v", csvRec.calls, tc.want.csvCalls)
			}
			if !equalCalls(spRec.calls, tc.want.spCalls) {
				t.Errorf("sp calls = %v, want %v", spRec.calls, tc.want.spCalls)
			}
		})
	}
}

func equalCalls(got, want [][]string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if len(got[i]) != len(want[i]) {
			return false
		}
		for j := range got[i] {
			if got[i][j] != want[i][j] {
				return false
			}
		}
	}
	return true
}
