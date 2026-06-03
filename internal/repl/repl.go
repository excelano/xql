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
// Stderr separate user-facing data from diagnostics so --format=json (etc.)
// can pipe cleanly to a downstream tool. Execute / Describe / Refresh are
// required; SetConfirm is optional (writes will fall through with no prompt
// when nil, which matches the no-prompt behavior in --exec mode).
type Session struct {
	Out         io.Writer
	Stderr      io.Writer
	Prompt      string // e.g. "xql csv> "
	HistoryPath string // file path; loaded at start, written on exit
	Banner      string // shown once after history loads, before the first prompt

	Execute    func(stmt parse.Stmt, commit bool) error
	Describe   func(w io.Writer) error
	Refresh    func() error
	SetConfirm func(fn func() bool)
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

		switch classifyMeta(trimmed) {
		case metaCmdQuit:
			return nil
		case metaCmdHelp:
			printHelp(s.Out)
			continue
		case metaCmdDescribe:
			if err := s.Describe(s.Out); err != nil {
				fmt.Fprintf(s.Stderr, "Error: %v\n", err)
			}
			continue
		case metaCmdRefresh:
			if err := s.Refresh(); err != nil {
				fmt.Fprintf(s.Stderr, "Error: %v\n", err)
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

type metaCmd int

const (
	metaCmdNone metaCmd = iota
	metaCmdQuit
	metaCmdHelp
	metaCmdDescribe
	metaCmdRefresh
)

// classifyMeta recognizes the small set of word-form meta commands. Plain
// words (quit, help, describe, refresh) are intentional — psql-style \-
// commands feel out of place here, and these don't collide with the SQL
// dialect's keywords.
func classifyMeta(line string) metaCmd {
	cmd := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";"))
	switch strings.ToUpper(cmd) {
	case "QUIT", "EXIT":
		return metaCmdQuit
	case "HELP", "?":
		return metaCmdHelp
	case "DESCRIBE":
		return metaCmdDescribe
	case "REFRESH":
		return metaCmdRefresh
	}
	return metaCmdNone
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
  describe           Print the bound table's columns and inferred types.
  refresh            Re-read the bound table from its source.`)
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
