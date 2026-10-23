// Author: David M. Anderson
// Built with AI assistance (Claude, Anthropic)

package sp

import (
	"encoding/json"
	"testing"
)

func TestIsSystemColumn(t *testing.T) {
	raw := json.RawMessage(`{}`)
	tests := []struct {
		col  columnJSON
		want bool
	}{
		{columnJSON{Name: "Title"}, false},
		{columnJSON{Name: "ID", ReadOnly: true}, false},
		{columnJSON{Name: "Author", ReadOnly: true, PersonOrGroup: &raw}, false},
		{columnJSON{Name: "Total", ReadOnly: true, Calculated: &raw}, false},
		{columnJSON{Name: "LinkTitle", ReadOnly: true}, true},
		{columnJSON{Name: "_UIVersionString", ReadOnly: true}, true},
		{columnJSON{Name: "ContentType", ColumnGroup: "_Hidden"}, true},
		{columnJSON{Name: "Attachments"}, true},
	}
	for _, tt := range tests {
		if got := isSystemColumn(&tt.col); got != tt.want {
			t.Errorf("isSystemColumn(%s) = %v, want %v", tt.col.Name, got, tt.want)
		}
	}
}

func TestAllowsMultipleValues(t *testing.T) {
	single := json.RawMessage(`{"allowMultipleValues":false}`)
	multi := json.RawMessage(`{"allowMultipleValues":true}`)
	if allowsMultipleValues(&columnJSON{PersonOrGroup: &single}) {
		t.Error("single-value person reported as multi")
	}
	if !allowsMultipleValues(&columnJSON{Lookup: &multi}) {
		t.Error("multi-value lookup not reported as multi")
	}
}

func TestNormalizeFields(t *testing.T) {
	schema := map[string]FieldInfo{
		"ID":        {Name: "ID", Type: FieldNumber},
		"Author":    {Name: "Author", Type: FieldPerson},
		"Reviewers": {Name: "Reviewers", Type: FieldPerson, Multi: true},
	}
	fields := map[string]any{
		"id":                "7",
		"AuthorLookupId":    "11",
		"ReviewersLookupId": "12",
		"Title":             "x",
	}
	normalizeFields(fields, schema)
	if fields["ID"] != 7.0 {
		t.Errorf("ID = %#v, want 7", fields["ID"])
	}
	if fields["Author"] != 11.0 {
		t.Errorf("Author = %#v, want 11", fields["Author"])
	}
	if _, ok := fields["Reviewers"]; ok {
		t.Errorf("multi-value Reviewers was filled from its LookupId: %#v", fields["Reviewers"])
	}
}
