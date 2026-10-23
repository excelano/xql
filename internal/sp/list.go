// Author: David M. Anderson
// Built with AI assistance (Claude, Anthropic)

// Package sp is the SharePoint backend for xql: list binding (site + list
// resolution + column schema), OData translation, and the read/write executor.
// Sign-in and the Graph HTTP client come from github.com/excelano/spauth, the
// layer this backend shares with the xfiles tools.
package sp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/excelano/spauth"
)

// FieldType is a coarse classification of a SharePoint column, derived from
// Graph's per-type discriminator subobjects on the column resource (text,
// number, dateTime, ...). Used by the OData translator to format comparisons
// correctly and by the write path to reject writes to unsupported types.
type FieldType string

const (
	FieldText       FieldType = "text"
	FieldNote       FieldType = "note"
	FieldNumber     FieldType = "number"
	FieldBoolean    FieldType = "boolean"
	FieldDateTime   FieldType = "dateTime"
	FieldChoice     FieldType = "choice"
	FieldPerson     FieldType = "person"
	FieldLookup     FieldType = "lookup"
	FieldHyperlink  FieldType = "hyperlink"
	FieldCalculated FieldType = "calculated"
	FieldUnknown    FieldType = "unknown"
)

// FieldInfo describes one column. Name is the internal name used in Graph
// $filter expressions and PATCH bodies; DisplayName is what the SharePoint UI
// shows.
type FieldInfo struct {
	Name        string
	DisplayName string
	Type        FieldType
	Hidden      bool
	ReadOnly    bool
	Required    bool
	Multi       bool
}

// BoundList is the resolved single list this REPL session operates on.
// Columns preserves Graph's response order (creation order in SharePoint) so
// SELECT * renders columns predictably; Schema is the lookup map keyed by
// internal name. SourceURL is the original URL passed to ResolveListBinding,
// kept so the REPL's refresh command can re-bind without re-asking the user.
type BoundList struct {
	SiteID      string
	ListID      string
	Name        string
	DisplayName string
	SourceURL   string
	Columns     []string
	Schema      map[string]FieldInfo
}

// parseListURL extracts hostname, site path, and list name from a SharePoint
// list URL. Accepts both bare list root URLs and AllItems.aspx variants, and
// URL-encoded list name segments.
func parseListURL(rawURL string) (hostname, sitePath, listName string, err error) {
	u, perr := url.Parse(rawURL)
	if perr != nil {
		return "", "", "", fmt.Errorf("parsing list URL: %w", perr)
	}
	if u.Host == "" {
		return "", "", "", fmt.Errorf("list URL has no host: %s", rawURL)
	}
	hostname = u.Host

	path := strings.Trim(u.Path, "/")
	if path == "" {
		return "", "", "", fmt.Errorf("list URL has no path: %s", rawURL)
	}
	parts := strings.Split(path, "/")

	listsIdx := -1
	for i, p := range parts {
		if strings.EqualFold(p, "Lists") {
			listsIdx = i
			break
		}
	}
	if listsIdx == -1 {
		return "", "", "", fmt.Errorf("URL missing '/Lists/' segment: %s", rawURL)
	}
	if listsIdx+1 >= len(parts) {
		return "", "", "", fmt.Errorf("URL has no list name after '/Lists/': %s", rawURL)
	}

	if listsIdx == 0 {
		sitePath = ""
	} else {
		sitePath = "/" + strings.Join(parts[:listsIdx], "/")
	}
	decoded, derr := url.PathUnescape(parts[listsIdx+1])
	if derr != nil {
		return "", "", "", fmt.Errorf("decoding list name segment %q: %w", parts[listsIdx+1], derr)
	}
	listName = decoded
	return hostname, sitePath, listName, nil
}

// ResolveListBinding resolves a SharePoint list URL to its Graph IDs and
// column schema in three calls: site, list, columns.
func ResolveListBinding(ctx context.Context, graph *spauth.GraphClient, listURL string) (*BoundList, error) {
	hostname, sitePath, listName, err := parseListURL(listURL)
	if err != nil {
		return nil, err
	}

	siteID, err := resolveSiteID(ctx, graph, hostname, sitePath)
	if err != nil {
		return nil, fmt.Errorf("resolving site: %w", err)
	}

	listID, foundName, displayName, err := resolveListID(ctx, graph, siteID, listName)
	if err != nil {
		return nil, fmt.Errorf("resolving list %q: %w", listName, err)
	}

	order, schema, err := fetchColumns(ctx, graph, siteID, listID)
	if err != nil {
		return nil, fmt.Errorf("fetching column schema: %w", err)
	}

	return &BoundList{
		SiteID:      siteID,
		ListID:      listID,
		Name:        foundName,
		DisplayName: displayName,
		SourceURL:   listURL,
		Columns:     order,
		Schema:      schema,
	}, nil
}

func resolveSiteID(ctx context.Context, graph *spauth.GraphClient, hostname, sitePath string) (string, error) {
	var path string
	if sitePath == "" {
		path = fmt.Sprintf("/sites/%s", hostname)
	} else {
		path = fmt.Sprintf("/sites/%s:%s", hostname, sitePath)
	}
	body, err := graph.Get(ctx, path, nil)
	if err != nil {
		return "", err
	}
	var site struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &site); err != nil {
		return "", fmt.Errorf("decoding site response: %w", err)
	}
	if site.ID == "" {
		return "", fmt.Errorf("site response missing id")
	}
	return site.ID, nil
}

// resolveListID locates a list on the site by either its system name or
// display name. Graph's /sites/{id}/lists endpoint rejects compound $filter
// expressions (and is finicky about even single-property ones), so we fetch
// the full list collection and match client-side. Sites rarely host more than
// a few dozen lists; pagination via getAll handles the long-tail case.
func resolveListID(ctx context.Context, graph *spauth.GraphClient, siteID, listName string) (id, name, displayName string, err error) {
	q := url.Values{
		"$select": {"id,name,displayName"},
	}
	raws, err := graph.GetAll(ctx, fmt.Sprintf("/sites/%s/lists", siteID), q)
	if err != nil {
		return "", "", "", err
	}

	type listRec struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}

	var matches []listRec
	for _, raw := range raws {
		var r listRec
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", "", "", fmt.Errorf("decoding list entry: %w", err)
		}
		if r.Name == listName || r.DisplayName == listName {
			matches = append(matches, r)
		}
	}

	if len(matches) == 0 {
		return "", "", "", fmt.Errorf("no list found matching %q (checked name and displayName across %d lists)", listName, len(raws))
	}
	if len(matches) > 1 {
		labels := make([]string, len(matches))
		for i, v := range matches {
			labels[i] = fmt.Sprintf("%s (name=%s)", v.DisplayName, v.Name)
		}
		return "", "", "", fmt.Errorf("multiple lists match %q: %s", listName, strings.Join(labels, "; "))
	}
	m := matches[0]
	return m.ID, m.Name, m.DisplayName, nil
}

func fetchColumns(ctx context.Context, graph *spauth.GraphClient, siteID, listID string) ([]string, map[string]FieldInfo, error) {
	raws, err := graph.GetAll(ctx, fmt.Sprintf("/sites/%s/lists/%s/columns", siteID, listID), nil)
	if err != nil {
		return nil, nil, err
	}
	order := make([]string, 0, len(raws))
	schema := make(map[string]FieldInfo, len(raws))
	for _, raw := range raws {
		var c columnJSON
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, nil, fmt.Errorf("decoding column: %w", err)
		}
		if c.Name == "" {
			continue
		}
		order = append(order, c.Name)
		schema[c.Name] = FieldInfo{
			Name:        c.Name,
			DisplayName: c.DisplayName,
			Type:        detectFieldType(&c),
			Hidden:      c.Hidden || isSystemColumn(&c),
			ReadOnly:    c.ReadOnly,
			Required:    c.Required,
			Multi:       allowsMultipleValues(&c),
		}
	}
	return order, schema, nil
}

type columnJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	ColumnGroup string `json:"columnGroup"`
	Hidden      bool   `json:"hidden"`
	ReadOnly    bool   `json:"readOnly"`
	Required    bool   `json:"required"`

	Text *struct {
		AllowMultipleLines bool `json:"allowMultipleLines"`
	} `json:"text,omitempty"`
	Number             *json.RawMessage `json:"number,omitempty"`
	Boolean            *json.RawMessage `json:"boolean,omitempty"`
	DateTime           *json.RawMessage `json:"dateTime,omitempty"`
	Choice             *json.RawMessage `json:"choice,omitempty"`
	PersonOrGroup      *json.RawMessage `json:"personOrGroup,omitempty"`
	Lookup             *json.RawMessage `json:"lookup,omitempty"`
	HyperlinkOrPicture *json.RawMessage `json:"hyperlinkOrPicture,omitempty"`
	Calculated         *json.RawMessage `json:"calculated,omitempty"`
}

// allowsMultipleValues reports whether a person or lookup column holds a
// list of values rather than one.
func allowsMultipleValues(c *columnJSON) bool {
	for _, facet := range []*json.RawMessage{c.PersonOrGroup, c.Lookup} {
		if facet == nil {
			continue
		}
		var f struct {
			AllowMultipleValues bool `json:"allowMultipleValues"`
		}
		if json.Unmarshal(*facet, &f) == nil && f.AllowMultipleValues {
			return true
		}
	}
	return false
}

// shownSystemColumns are the read-only system columns SELECT * keeps, being
// the ones a person asks about: the item number, its timestamps and who
// created and last changed it.
var shownSystemColumns = map[string]bool{
	"ID": true, "Created": true, "Modified": true, "Author": true, "Editor": true,
}

// isSystemColumn reports whether a column is SharePoint plumbing that the
// list UI keeps out of its default view. Graph marks almost none of these
// hidden, so read-only is the signal: a user cannot create a read-only
// column other than a calculated one. Attachments and the _Hidden group are
// writable-looking plumbing.
func isSystemColumn(c *columnJSON) bool {
	if shownSystemColumns[c.Name] {
		return false
	}
	if c.ColumnGroup == "_Hidden" || c.Name == "Attachments" {
		return true
	}
	return c.ReadOnly && c.Calculated == nil
}

func detectFieldType(c *columnJSON) FieldType {
	if c.Name == "ID" {
		return FieldNumber
	}
	switch {
	case c.Text != nil:
		if c.Text.AllowMultipleLines {
			return FieldNote
		}
		return FieldText
	case c.Number != nil:
		return FieldNumber
	case c.Boolean != nil:
		return FieldBoolean
	case c.DateTime != nil:
		return FieldDateTime
	case c.Choice != nil:
		return FieldChoice
	case c.PersonOrGroup != nil:
		return FieldPerson
	case c.Lookup != nil:
		return FieldLookup
	case c.HyperlinkOrPicture != nil:
		return FieldHyperlink
	case c.Calculated != nil:
		return FieldCalculated
	default:
		return FieldUnknown
	}
}

// normalizeFields fills in the values Graph returns under a different key
// than the column name: the item number arrives as "id", and a single-value
// person or lookup column as "<Name>LookupId", both as strings. Each is stored under
// its column name as a number, so it reads, sorts and compares like one.
func normalizeFields(fields map[string]any, schema map[string]FieldInfo) {
	if _, ok := fields["ID"]; !ok {
		if n, ok := numericString(fields["id"]); ok {
			fields["ID"] = n
		}
	}
	for name, info := range schema {
		if !info.isLookupID() {
			continue
		}
		if _, ok := fields[name]; ok {
			continue
		}
		if n, ok := numericString(fields[name+"LookupId"]); ok {
			fields[name] = n
		}
	}
}

// isLookupID reports whether a column reads and filters as the numeric ID of
// the one item or person it points at.
func (f FieldInfo) isLookupID() bool {
	return (f.Type == FieldPerson || f.Type == FieldLookup) && !f.Multi
}

func numericString(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	return n, err == nil
}

// escapeODataString doubles single quotes per OData literal-string rules.
// Used by the OData translator (slice 2); kept here with the other SharePoint
// metadata helpers.
func escapeODataString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
