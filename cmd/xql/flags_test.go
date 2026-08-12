package main

import (
	"flag"
	"testing"
)

func TestResolveMode(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		json  bool
		want  string
		isErr bool
	}{
		{name: "neither given leaves the mode blank"},
		{name: "mode alone passes through", mode: "csv", want: "csv"},
		{name: "json alone selects json", json: true, want: "json"},
		{name: "json with a matching mode agrees", mode: "json", json: true, want: "json"},
		{name: "json with a mixed-case mode agrees", mode: "JSON", json: true, want: "json"},
		{name: "json with a different mode conflicts", mode: "csv", json: true, isErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMode(tc.mode, tc.json)
			if tc.isErr {
				if err == nil {
					t.Fatalf("resolveMode(%q, %v) = %q, want an error", tc.mode, tc.json, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMode(%q, %v) returned %v", tc.mode, tc.json, err)
			}
			if got != tc.want {
				t.Errorf("resolveMode(%q, %v) = %q, want %q", tc.mode, tc.json, got, tc.want)
			}
		})
	}
}

func TestHelpRequested(t *testing.T) {
	newSet := func() *flag.FlagSet {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.String("exec", "", "statement")
		fs.Bool("describe", false, "describe")
		return fs
	}
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no arguments", args: nil, want: false},
		{name: "long form", args: []string{"--help"}, want: true},
		{name: "short form", args: []string{"-h"}, want: true},
		{name: "single-dash long form", args: []string{"-help"}, want: true},
		{name: "after a file", args: []string{"data.csv", "--help"}, want: true},
		{name: "after a bool flag", args: []string{"--describe", "--help"}, want: true},
		// The whole reason for walking rather than scanning: --help here is a
		// SQL statement, not a request for help.
		{name: "as a flag value", args: []string{"--exec", "--help"}, want: false},
		{name: "as an attached flag value", args: []string{"--exec=--help"}, want: false},
		{name: "a lone dash is not a flag", args: []string{"-"}, want: false},
		{name: "plain arguments", args: []string{"data.csv"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := helpRequested(tc.args, newSet()); got != tc.want {
				t.Errorf("helpRequested(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
