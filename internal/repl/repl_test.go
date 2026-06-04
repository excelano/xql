package repl

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseMeta_Recognized(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantArg  string
	}{
		{"quit", "quit", ""},
		{"EXIT;", "exit", ""},
		{"help", "help", ""},
		{"?", "?", ""},
		{"describe", "describe", ""},
		{"REFRESH ;", "refresh", ""},
		{"mode csv", "mode", "csv"},
		{"  Mode   TSV  ", "mode", "TSV"},
		{"headers off", "headers", "off"},
		{"headers on;", "headers", "on"},
		{"output 'results.csv'", "output", "results.csv"},
		{`output "path with spaces.csv"`, "output", "path with spaces.csv"},
		{"output /tmp/bare-path.csv", "output", "/tmp/bare-path.csv"},
		{"output", "output", ""},
		{"once '/tmp/once.csv'", "once", "/tmp/once.csv"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			m := parseMeta(tc.in)
			if m == nil {
				t.Fatalf("parseMeta(%q) = nil, want %q", tc.in, tc.wantName)
			}
			if m.name != tc.wantName {
				t.Errorf("name = %q, want %q", m.name, tc.wantName)
			}
			if m.arg != tc.wantArg {
				t.Errorf("arg = %q, want %q", m.arg, tc.wantArg)
			}
		})
	}
}

func TestParseMeta_NotMeta(t *testing.T) {
	cases := []string{
		"SELECT * FROM tbl",
		"UPDATE SET x = 1",
		"DELETE WHERE id = 1",
		"INSERT (a) VALUES (1)",
		"random text",
		"",
		"   ",
	}
	for _, in := range cases {
		if m := parseMeta(in); m != nil {
			t.Errorf("parseMeta(%q) = %+v, want nil", in, m)
		}
	}
}

func TestDispatchMeta_SetMode(t *testing.T) {
	f := newFake()
	s := f.s
	once := false

	quit, err := dispatchMeta(s, &metaCmd{name: "mode", arg: "csv"}, &once)
	if err != nil || quit {
		t.Fatalf("dispatch mode csv: quit=%v err=%v", quit, err)
	}
	if f.mode != "csv" {
		t.Errorf("mode = %q, want csv", f.mode)
	}
}

func TestDispatchMeta_ModeRejectsUnknown(t *testing.T) {
	f := newFake()
	_, err := dispatchMeta(f.s, &metaCmd{name: "mode", arg: "yaml"}, new(bool))
	if err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("expected unknown-mode error, got %v", err)
	}
	if f.mode != "" {
		t.Errorf("unknown mode should not mutate state: %q", f.mode)
	}
}

func TestDispatchMeta_ModeUsage(t *testing.T) {
	f := newFake()
	_, err := dispatchMeta(f.s, &metaCmd{name: "mode", arg: ""}, new(bool))
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestDispatchMeta_HeadersOnOff(t *testing.T) {
	f := newFake()
	if _, err := dispatchMeta(f.s, &metaCmd{name: "headers", arg: "on"}, new(bool)); err != nil {
		t.Fatalf("headers on: %v", err)
	}
	if !f.headers {
		t.Error("headers on did not set true")
	}
	if _, err := dispatchMeta(f.s, &metaCmd{name: "headers", arg: "off"}, new(bool)); err != nil {
		t.Fatalf("headers off: %v", err)
	}
	if f.headers {
		t.Error("headers off did not set false")
	}
}

func TestDispatchMeta_HeadersBadValue(t *testing.T) {
	f := newFake()
	_, err := dispatchMeta(f.s, &metaCmd{name: "headers", arg: "yes"}, new(bool))
	if err == nil || !strings.Contains(err.Error(), "on or off") {
		t.Fatalf("expected on-or-off error, got %v", err)
	}
}

func TestDispatchMeta_OutputSticky(t *testing.T) {
	f := newFake()
	armed := false
	if _, err := dispatchMeta(f.s, &metaCmd{name: "output", arg: "/tmp/x.csv"}, &armed); err != nil {
		t.Fatalf("output set: %v", err)
	}
	if f.output != "/tmp/x.csv" {
		t.Errorf("output path = %q, want /tmp/x.csv", f.output)
	}
	if armed {
		t.Error("sticky output should not arm once")
	}
}

func TestDispatchMeta_OutputClears(t *testing.T) {
	f := newFake()
	f.output = "/tmp/old.csv"
	armed := true
	if _, err := dispatchMeta(f.s, &metaCmd{name: "output", arg: ""}, &armed); err != nil {
		t.Fatalf("output clear: %v", err)
	}
	if f.output != "" {
		t.Errorf("output path = %q, want empty", f.output)
	}
	if armed {
		t.Error("output (no-arg) should disarm once")
	}
}

func TestDispatchMeta_OnceArmsThenDisarmsAfterSQL(t *testing.T) {
	// dispatchMeta arms once; Run() is responsible for clearing after
	// SQL execution. This test only verifies the meta layer arms.
	f := newFake()
	armed := false
	if _, err := dispatchMeta(f.s, &metaCmd{name: "once", arg: "/tmp/once.csv"}, &armed); err != nil {
		t.Fatalf("once: %v", err)
	}
	if !armed {
		t.Error("once should arm the onceArmed flag")
	}
	if f.output != "/tmp/once.csv" {
		t.Errorf("output path = %q, want /tmp/once.csv", f.output)
	}
}

func TestDispatchMeta_OnceRequiresPath(t *testing.T) {
	f := newFake()
	armed := false
	_, err := dispatchMeta(f.s, &metaCmd{name: "once", arg: ""}, &armed)
	if err == nil || !strings.Contains(err.Error(), "path required") {
		t.Fatalf("expected path-required error, got %v", err)
	}
	if armed {
		t.Error("failed once should not arm")
	}
}

func TestDispatchMeta_Quit(t *testing.T) {
	f := newFake()
	quit, err := dispatchMeta(f.s, &metaCmd{name: "quit"}, new(bool))
	if err != nil {
		t.Fatalf("quit err: %v", err)
	}
	if !quit {
		t.Error("quit should return quit=true")
	}
}

func TestDispatchMeta_ModeRejectedIfNoSetter(t *testing.T) {
	// A backend that omits SetMode should refuse mode changes cleanly
	// rather than silently no-op.
	s := &Session{Out: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	_, err := dispatchMeta(s, &metaCmd{name: "mode", arg: "csv"}, new(bool))
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected unsupported error, got %v", err)
	}
}

// fakeState wraps a Session whose Set* callbacks record into the embedded
// fields, so a test can observe the meta-command's effect.
type fakeState struct {
	out, errOut bytes.Buffer
	mode        string
	headers     bool
	output      string

	s *Session
}

func newFake() *fakeState {
	f := &fakeState{}
	f.s = &Session{
		Out:           &f.out,
		Stderr:        &f.errOut,
		SetMode:       func(m string) { f.mode = m },
		SetHeaders:    func(on bool) { f.headers = on },
		SetOutputPath: func(p string) { f.output = p },
	}
	return f
}
