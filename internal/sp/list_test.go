// Author: David M. Anderson
// Built with AI assistance (Claude, Anthropic)

package sp

import (
	"encoding/json"
	"testing"

	"github.com/excelano/xql/internal/cell"
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
		{columnJSON{Name: "Total", ReadOnly: true, Calculated: &struct {
			OutputType string `json:"outputType"`
		}{"number"}}, false},
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
		"Readers":   {Name: "Readers", Type: FieldPerson, Multi: true},
	}
	fields := map[string]any{
		"Readers":           []any{map[string]any{"LookupId": 11.0, "LookupValue": "Ann"}, map[string]any{"LookupId": 12.0, "LookupValue": "Bo"}},
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
	if fields["Readers"] != "Ann; Bo" {
		t.Errorf("Readers = %#v, want \"Ann; Bo\"", fields["Readers"])
	}
	if _, ok := fields["Reviewers"]; ok {
		t.Errorf("multi-value Reviewers was filled from its LookupId: %#v", fields["Reviewers"])
	}
}

func TestColumnFacets(t *testing.T) {
	var cols []columnJSON
	if err := json.Unmarshal([]byte(`[
		{"name":"Status","choice":{"allowTextEntry":false,"choices":["Open","Closed"]}},
		{"name":"Tag","choice":{"allowTextEntry":true,"choices":["a"]}},
		{"name":"Reviewers","personOrGroup":{"allowMultipleSelection":true}},
		{"name":"Total","readOnly":true,"calculated":{"outputType":"number"}}
	]`), &cols); err != nil {
		t.Fatal(err)
	}
	if got := closedChoices(&cols[0]); len(got) != 2 {
		t.Errorf("closed choice: got %v", got)
	}
	if got := closedChoices(&cols[1]); got != nil {
		t.Errorf("open choice: got %v, want nil", got)
	}
	if !allowsMultipleValues(&cols[2]) {
		t.Error("allowMultipleSelection person not reported as multi")
	}
	total := FieldInfo{Type: detectFieldType(&cols[3]), Output: calculatedOutput(&cols[3])}
	if total.CellType() != cell.TypeFloat {
		t.Errorf("number calculated column reads as %s, want float", total.CellType())
	}
}
