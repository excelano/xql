// Package repl drives the interactive prompt shared across backends. Each
// backend provides Execute / Describe / Refresh closures plus a banner and a
// prompt string; the REPL handles line editing, history, meta-commands, the
// y/N confirmation flow for writes, and parse-error formatting.
package repl

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterh/liner"

	"github.com/excelano/xql/internal/parse"
)

// Session bundles everything the REPL needs from its host backend. Out and
// Stderr separate user-facing data from diagnostics so --mode=json (etc.)
// can pipe cleanly to a downstream tool. Execute / Describe / Refresh are
// required; the Set* callbacks are optional — a backend that omits one will
// reject the corresponding meta-command at runtime.
type Session struct {
	Out         io.Writer
	Stderr      io.Writer
	Prompt      string // e.g. "xql> "
	HistoryPath string // file path; loaded at start, written on exit
	Banner      string // shown once after history loads, before the first prompt

	Execute    func(stmt parse.Stmt, commit bool) error
	Describe   func(w io.Writer, arg string) error
	Refresh    func() error
	SetConfirm func(fn func() bool)

	// SetMode, SetHeaders, SetOutputPath bridge the REPL meta-commands
	// `mode`, `headers`, `once`, and `output` to the host executor's state.
	// A nil setter means the backend does not support that knob.
	SetMode       func(mode string)
	SetHeaders    func(on bool)
	SetOutputPath func(path string)

	// SetAllFields / GetAllFields back the REPL's `set all-fields on|off`
	// meta-command (and its bare-form state report). Backends that have
	// no hidden-field concept (CSV) leave both nil; the REPL then rejects
	// the meta-command with a "backend does not support" error.
	SetAllFields func(on bool)
	GetAllFields func() bool
}

// Run drives the prompt loop until ^D, "quit", or an unrecoverable read
// error. Per-statement errors print to Stderr and the loop continues.
func Run(s *Session) error {
	line := liner.NewLiner()
	defer line.Close()
	line.SetCtrlCAborts(true)

	loadHistory(line, s.HistoryPath)
	defer saveHistory(line, s.HistoryPath)

	if s.SetConfirm != nil {
		s.SetConfirm(func() bool {
			ans, err := line.Prompt("Apply? [y/N]: ")
			if err != nil {
				return false
			}
			ans = strings.ToLower(strings.TrimSpace(ans))
			return ans == "y" || ans == "yes"
		})
	}

	if s.Banner != "" {
		fmt.Fprintln(s.Stderr, s.Banner)
	}

	// onceArmed tracks whether the next SQL statement should reset
	// OutputPath after running. `once 'file'` arms it; `output ...` or a
	// completed SQL execution disarms it.
	onceArmed := false

	for {
		input, err := line.Prompt(s.Prompt)
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(s.Stderr)
			return nil
		}
		if errors.Is(err, liner.ErrPromptAborted) {
			continue
		}
		if err != nil {
			return err
		}

		trimmed := strings.TrimSpace(input)
		if trimmed == "" {
			continue
		}
		line.AppendHistory(trimmed)

		if m := parseMeta(trimmed); m != nil {
			quit, mErr := dispatchMeta(s, m, &onceArmed)
			if mErr != nil {
				fmt.Fprintf(s.Stderr, "Error: %v\n", mErr)
			}
			if quit {
				return nil
			}
			continue
		}

		cleaned, commit := parse.PreProcess(trimmed)
		if cleaned == "" {
			continue
		}
		stmt, perr := parse.Parse(cleaned)
		if perr != nil {
			printParseError(s.Stderr, cleaned, perr)
			continue
		}
		if err := s.Execute(stmt, commit); err != nil {
			fmt.Fprintf(s.Stderr, "Error: %v\n", err)
		}
		// Disarm `once` after the SQL statement runs (success or fail) so
		// subsequent statements return to the prior output target.
		if onceArmed && s.SetOutputPath != nil {
			s.SetOutputPath("")
			onceArmed = false
		}
	}
}

func loadHistory(line *liner.State, path string) {
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = line.ReadHistory(f)
}

func saveHistory(line *liner.State, path string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = line.WriteHistory(f)
}

// metaCmd is a parsed REPL meta-command. The set of recognized names is
// fixed; arg holds the rest of the line, with surrounding quotes stripped
// when present.
type metaCmd struct {
	name string
	arg  string
}

// parseMeta recognizes the bare-word meta commands. Plain words (no
// leading dot or backslash) keep the prompt feeling like a SQL shell rather
// than psql/sqlite; the names chosen don't collide with the SQL dialect's
// keywords. Returns nil when the line is not a meta command — the caller
// then routes it through the SQL parser.
func parseMeta(line string) *metaCmd {
	line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";"))
	if line == "" {
		return nil
	}
	head, rest := splitFirstWord(line)
	name := strings.ToLower(head)
	switch name {
	case "quit", "exit", "help", "?", "describe", "refresh",
		"mode", "headers", "once", "output", "set":
		return &metaCmd{name: name, arg: unquote(rest)}
	}
	return nil
}

// splitFirstWord returns the leading whitespace-delimited token and the
// trimmed remainder. Used to peel off the meta-command name.
func splitFirstWord(s string) (head, rest string) {
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// unquote strips a surrounding pair of single or double quotes. Inner
// content is returned verbatim — no escape processing, so paths with
// literal backslashes round-trip. Unbalanced quotes pass through as-is for
// the dispatcher to surface as an error if it cares.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// dispatchMeta runs a parsed meta command against the session. quit=true
// asks Run to exit the loop. err is non-nil when the command was malformed
// or the backend doesn't support that knob.
func dispatchMeta(s *Session, m *metaCmd, onceArmed *bool) (quit bool, err error) {
	switch m.name {
	case "quit", "exit":
		return true, nil
	case "help", "?":
		printHelp(s.Out)
		return false, nil
	case "describe":
		return false, s.Describe(s.Out, m.arg)
	case "refresh":
		return false, s.Refresh()
	case "mode":
		return false, applyMode(s, m.arg)
	case "headers":
		return false, applyHeaders(s, m.arg)
	case "output":
		return false, applyOutput(s, m.arg, onceArmed)
	case "once":
		return false, applyOnce(s, m.arg, onceArmed)
	case "set":
		return false, applySet(s, m.arg)
	}
	return false, fmt.Errorf("internal: unhandled meta command %q", m.name)
}

func applyMode(s *Session, arg string) error {
	if arg == "" {
		return fmt.Errorf("mode: usage: mode <table|tsv|csv|json>")
	}
	switch arg {
	case "table", "tsv", "csv", "json":
	default:
		return fmt.Errorf("unknown mode %q (want table, tsv, csv, or json)", arg)
	}
	if s.SetMode == nil {
		return fmt.Errorf("backend does not support changing mode at runtime")
	}
	s.SetMode(arg)
	return nil
}

func applyHeaders(s *Session, arg string) error {
	if s.SetHeaders == nil {
		return fmt.Errorf("backend does not support headers")
	}
	switch strings.ToLower(arg) {
	case "on":
		s.SetHeaders(true)
	case "off":
		s.SetHeaders(false)
	case "":
		return fmt.Errorf("headers: usage: headers on|off")
	default:
		return fmt.Errorf("headers: expected on or off, got %q", arg)
	}
	return nil
}

func applyOutput(s *Session, arg string, onceArmed *bool) error {
	if s.SetOutputPath == nil {
		return fmt.Errorf("backend does not support --output")
	}
	// `output` no-arg clears; `output PATH` redirects (sticky) and
	// truncates the file once so subsequent SELECTs accumulate (the
	// executor opens for append). Either way, an explicit OUTPUT
	// cancels any pending once.
	if arg != "" {
		if err := TruncateOutputFile(arg); err != nil {
			return err
		}
	}
	s.SetOutputPath(arg)
	*onceArmed = false
	return nil
}

// applySet implements the `set` meta-command. Bare `set` lists every
// known toggle with its current state. `set <name>` reports just that
// toggle. `set <name> <value>` flips it. Bare-form reads-state, no-args-no-
// state-change matches the convention the other meta-commands use.
func applySet(s *Session, arg string) error {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return reportAllToggles(s)
	}
	head, rest := splitFirstWord(arg)
	name := strings.ToLower(head)
	switch name {
	case "all-fields":
		return applySetAllFields(s, strings.TrimSpace(rest))
	}
	return fmt.Errorf("set: unknown toggle %q (try bare `set` to list)", head)
}

func reportAllToggles(s *Session) error {
	any := false
	if s.GetAllFields != nil {
		fmt.Fprintf(s.Out, "all-fields: %s\n", onOff(s.GetAllFields()))
		any = true
	}
	if !any {
		return fmt.Errorf("set: backend has no runtime toggles")
	}
	return nil
}

func applySetAllFields(s *Session, value string) error {
	if s.SetAllFields == nil || s.GetAllFields == nil {
		return fmt.Errorf("set: backend does not support all-fields")
	}
	if value == "" {
		fmt.Fprintf(s.Out, "all-fields: %s\n", onOff(s.GetAllFields()))
		return nil
	}
	switch strings.ToLower(value) {
	case "on", "true", "1", "yes":
		s.SetAllFields(true)
	case "off", "false", "0", "no":
		s.SetAllFields(false)
	default:
		return fmt.Errorf("set all-fields: expected on or off, got %q", value)
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func applyOnce(s *Session, arg string, onceArmed *bool) error {
	if s.SetOutputPath == nil {
		return fmt.Errorf("backend does not support --output")
	}
	if arg == "" {
		return fmt.Errorf("once: path required (e.g. once 'results.csv')")
	}
	if err := TruncateOutputFile(arg); err != nil {
		return err
	}
	s.SetOutputPath(arg)
	*onceArmed = true
	return nil
}

// TruncateOutputFile resets the output file's contents so the next render
// starts from an empty file. The executor opens with O_APPEND, so without
// this reset a fresh `output 'FILE'` (or CLI --output) would tack onto
// whatever was there. Exported so cmd/xql can call it from the CLI path.
func TruncateOutputFile(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("truncate output file: %w", err)
	}
	return f.Close()
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `Statements (one per line; trailing ';' optional):
  SELECT [* | col1, col2, ...] [WHERE pred]
  UPDATE SET col = val [, col = val ...] [WHERE pred]
  DELETE [WHERE pred]
  INSERT (col1, col2, ...) VALUES (val1, val2, ...)

Writes preview by default and prompt "Apply? [y/N]" before committing.
Append '!' to skip the prompt and commit immediately
(e.g. "DELETE WHERE Status = 'Archived' !").

Meta-commands (case-insensitive):
  quit, exit         Exit the REPL.
  help, ?            This help.
  describe           Print the bound source's columns and inferred types.
  describe all       Include SharePoint system/hidden columns.
  refresh            Re-read the bound source.
  mode <name>        Set stdout render mode: table, tsv, csv, json.
  headers on|off     Show or hide column headers in row-shaped output.
  output 'PATH'      Redirect statement results to PATH as CSV (sticky).
  output             Clear redirect; results return to stdout.
  once 'PATH'        Redirect the NEXT statement only, then revert.
  set                Show current state of every runtime toggle.
  set <name>         Show current state of one toggle (e.g. set all-fields).
  set <name> on|off  Flip a toggle (currently: all-fields on the sp backend).`)
}

func printParseError(out io.Writer, input string, err error) {
	pe, ok := err.(*parse.ParseError)
	if !ok {
		fmt.Fprintf(out, "Parse error: %v\n", err)
		return
	}
	fmt.Fprintf(out, "Parse error: %s\n", pe.Msg)
	fmt.Fprintf(out, "  %s\n", input)
	if pe.Pos >= 0 && pe.Pos <= len(input) {
		fmt.Fprintf(out, "  %s^\n", strings.Repeat(" ", pe.Pos))
	}
}
