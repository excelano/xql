package sp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
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
func ResolveListBinding(ctx context.Context, graph *GraphClient, listURL string) (*BoundList, error) {
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

func resolveSiteID(ctx context.Context, graph *GraphClient, hostname, sitePath string) (string, error) {
	var path string
	if sitePath == "" {
		path = fmt.Sprintf("/sites/%s", hostname)
	} else {
		path = fmt.Sprintf("/sites/%s:%s", hostname, sitePath)
	}
	body, err := graph.get(ctx, path, nil)
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
func resolveListID(ctx context.Context, graph *GraphClient, siteID, listName string) (id, name, displayName string, err error) {
	q := url.Values{
		"$select": {"id,name,displayName"},
	}
	raws, err := graph.getAll(ctx, fmt.Sprintf("/sites/%s/lists", siteID), q)
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

func fetchColumns(ctx context.Context, graph *GraphClient, siteID, listID string) ([]string, map[string]FieldInfo, error) {
	raws, err := graph.getAll(ctx, fmt.Sprintf("/sites/%s/lists/%s/columns", siteID, listID), nil)
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
			Hidden:      c.Hidden,
			ReadOnly:    c.ReadOnly,
			Required:    c.Required,
		}
	}
	return order, schema, nil
}

type columnJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
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

func detectFieldType(c *columnJSON) FieldType {
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

// escapeODataString doubles single quotes per OData literal-string rules.
// Used by the OData translator (slice 2); kept here with the other SharePoint
// metadata helpers.
func escapeODataString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
