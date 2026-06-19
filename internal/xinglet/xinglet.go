// Package xinglet is the Xinglet backend for xql: a read-only HTTP shim over
// the xinglist export endpoint at <base>/home/xinglist-export.php. The remote
// list is fetched as CSV (with inline type annotations in the header), the
// type syntax is translated into xql's type-hint map, and the body is handed
// to the shared CSV loader -- so the executor, parser, and renderer are the
// CSV backend's verbatim.
//
// Auth is a single Bearer token from $XINGLET_TOKEN. The endpoint is
// session-or-bearer on the server side; we only ever speak Bearer.
package xinglet

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/excelano/xql/internal/cell"
	csvbackend "github.com/excelano/xql/internal/csv"
)

// DefaultBaseURL is used when XINGLET_BASE_URL is unset. Self-hosters point
// at their own host with the env var.
const DefaultBaseURL = "https://xinglet.com"

// uuidPattern accepts the standard 8-4-4-4-12 hex form, case-insensitive.
// We could call out to google/uuid to also enforce variant + version bits,
// but the server already 404s on any lookup miss; the local check exists
// only to catch typos before a network round-trip.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Config carries everything LoadList needs to issue one HTTP fetch.
// UserAgent is optional; if empty, no header is sent. HTTPClient is
// optional; nil falls back to a 30-second-timeout client.
type Config struct {
	BaseURL    string
	Token      string
	UserAgent  string
	HTTPClient *http.Client
}

// ParseURL pulls the UUID out of an "xinglet://<uuid>" string. The error is
// shaped for direct printing to the user -- the caller doesn't wrap it.
func ParseURL(s string) (string, error) {
	const prefix = "xinglet://"
	if !strings.HasPrefix(s, prefix) {
		return "", fmt.Errorf("xinglet URL must start with %q, got %q", prefix, s)
	}
	id := strings.TrimPrefix(s, prefix)
	if id == "" {
		return "", fmt.Errorf("xinglet URL is missing its uuid (expected %s<uuid>)", prefix)
	}
	if !uuidPattern.MatchString(id) {
		return "", fmt.Errorf("xinglet URL has malformed uuid %q (expected 8-4-4-4-12 hex)", id)
	}
	return id, nil
}

// LoadList fetches the named xinglist from the configured endpoint, parses
// the inline header type syntax, and returns a fully-typed cell.Table. The
// table's Path is set to "xinglet://<uuid>" so it identifies cleanly in
// REPL banners and Refresh output.
func LoadList(cfg Config, uuid string) (*cell.Table, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	url := base + "/home/xinglist-export.php?id=" + uuid

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Accept", "text/csv")
	if cfg.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.UserAgent)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", base, err)
	}
	defer resp.Body.Close()

	if err := classifyStatus(resp.StatusCode, uuid); err != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	bareHeader, hints, rest, err := translateHeader(body)
	if err != nil {
		return nil, err
	}

	rebuilt := bareHeader + "\n" + rest
	label := "xinglet://" + uuid
	return csvbackend.LoadCSVReader(label, strings.NewReader(rebuilt), csvbackend.LoadOptions{
		TypeHints: hints,
	})
}

// classifyStatus turns an HTTP status into a user-facing error. 200 returns
// nil; non-2xx returns a specific message for the known auth/access codes
// and a generic message otherwise.
func classifyStatus(status int, uuid string) error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("authentication failed (XINGLET_TOKEN missing, invalid, or revoked)")
	case http.StatusForbidden:
		return fmt.Errorf("access denied to xinglet %s (you may not be a member)", uuid)
	case http.StatusNotFound:
		return fmt.Errorf("xinglet not found: %s", uuid)
	default:
		return fmt.Errorf("server returned HTTP %d", status)
	}
}

// translateHeader reads the first CSV record of body, peels each header
// cell's inline type annotation (matching the server-side encoder in
// xinglist_csv_header_cell), and returns:
//   - bareHeader: a CSV-encoded line of just the column names
//   - hints:      a TypeHints map for the CSV loader
//   - rest:       everything after the first newline of body, untouched
//
// The header cell grammar (inverse of the server encoder):
//
//	name                    -> text column
//	name:number             -> number column
//	name:date               -> date column
//	name:choice             -> text column (option list omitted; pipe-unsafe)
//	name:choice(a|b|c)      -> text column (option list dropped on the client)
//	name:<anything-else>    -> literal column name "name:<anything-else>"
//
// The "anything-else" rule matches the server's tolerance for colons inside
// real column names.
func translateHeader(body []byte) (bareHeader string, hints map[string]cell.ColumnType, rest string, err error) {
	nl := indexNewline(body)
	if nl < 0 {
		return "", nil, "", fmt.Errorf("empty response (no header row)")
	}
	headerLine := string(body[:nl])
	rest = string(body[nl+1:])

	cells, err := parseHeaderRow(headerLine)
	if err != nil {
		return "", nil, "", err
	}

	bareNames := make([]string, len(cells))
	hints = make(map[string]cell.ColumnType, len(cells))
	for i, raw := range cells {
		name, t, ok := classifyHeaderCell(raw)
		bareNames[i] = name
		if ok {
			hints[name] = t
		}
	}

	bareHeader = encodeHeaderRow(bareNames)
	return bareHeader, hints, rest, nil
}

// classifyHeaderCell applies the header-cell grammar above to one raw cell.
// Returns the bare column name, the type hint, and whether a hint was found
// (ok=false means "no recognized annotation; leave inference to handle it").
func classifyHeaderCell(raw string) (name string, t cell.ColumnType, ok bool) {
	colon := strings.Index(raw, ":")
	if colon < 0 {
		return raw, 0, false
	}
	prefix := raw[:colon]
	suffix := raw[colon+1:]

	switch {
	case suffix == "number":
		return prefix, cell.TypeFloat, true
	case suffix == "date":
		return prefix, cell.TypeDate, true
	case suffix == "choice":
		return prefix, cell.TypeString, true
	case strings.HasPrefix(suffix, "choice("):
		return prefix, cell.TypeString, true
	}
	return raw, 0, false
}

// indexNewline returns the index of the first '\n' in b, or -1. The line
// before it may end with '\r' (Windows-style); the CSV reader handles that
// downstream, we only need to find the line break.
func indexNewline(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

// parseHeaderRow parses one CSV-encoded line into its cells. The line may
// be quoted (column names containing commas, quotes, or colons are quoted
// upstream by fputcsv), so we route it through encoding/csv rather than
// strings.Split.
func parseHeaderRow(line string) ([]string, error) {
	r := csv.NewReader(strings.NewReader(line))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rec, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("parsing header row: %w", err)
	}
	return rec, nil
}

// encodeHeaderRow re-encodes the bare column names as a CSV line. The
// downstream LoadCSVReader will parse it back; round-tripping through the
// same encoder guarantees the quoting matches.
func encodeHeaderRow(names []string) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(names)
	w.Flush()
	// csv.Writer appends "\n" after the record; strip it so the caller's
	// "\n" + rest concatenation doesn't produce a blank line.
	return strings.TrimRight(buf.String(), "\r\n")
}
