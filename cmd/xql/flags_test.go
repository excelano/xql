package main

import "testing"

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
