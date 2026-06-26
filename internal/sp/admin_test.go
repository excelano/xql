package sp

import (
	"bytes"
	"strings"
	"testing"
)

// describeTestExecutor builds an Executor whose schema mirrors the CSV-import
// pattern: a Title column (display == internal), three field_N columns whose
// display names came from the imported header row, and one hidden system
// column. Enough to exercise every branch of Describe.
func describeTestExecutor(allFields bool) *Executor {
	out := &bytes.Buffer{}
	return &Executor{
		Bound: &BoundList{
			Columns: []string{"Title", "field_1", "field_2", "_ColorTag", "field_3"},
			Schema: map[string]FieldInfo{
				"Title":     {Name: "Title", DisplayName: "Title", Type: FieldText},
				"field_1":   {Name: "field_1", DisplayName: "vendor", Type: FieldText},
				"field_2":   {Name: "field_2", DisplayName: "yearly_cost", Type: FieldNumber},
				"_ColorTag": {Name: "_ColorTag", DisplayName: "Color Tag", Type: FieldText, Hidden: true, ReadOnly: true},
				"field_3":   {Name: "field_3", DisplayName: "state", Type: FieldText},
			},
		},
		AllFields: allFields,
		Mode:      "table",
		Out:       out,
	}
}

func TestDescribeHidesHiddenByDefault(t *testing.T) {
	e := describeTestExecutor(false)
	buf := &bytes.Buffer{}
	if err := e.Describe(buf, ""); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "_ColorTag") || strings.Contains(out, "Color Tag") {
		t.Errorf("default describe should hide _ColorTag:\n%s", out)
	}
	if !strings.Contains(out, "vendor") || !strings.Contains(out, "yearly_cost") {
		t.Errorf("describe should show user-facing display names:\n%s", out)
	}
}

func TestDescribeAllIncludesHidden(t *testing.T) {
	e := describeTestExecutor(false)
	buf := &bytes.Buffer{}
	if err := e.Describe(buf, "all"); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Color Tag") || !strings.Contains(out, "_ColorTag") {
		t.Errorf("describe all should include hidden column with internal name:\n%s", out)
	}
}

func TestDescribeShowsInternalWhenDifferent(t *testing.T) {
	e := describeTestExecutor(false)
	buf := &bytes.Buffer{}
	if err := e.Describe(buf, ""); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "internal") {
		t.Errorf("describe should add an 'internal' column when display != name:\n%s", out)
	}
	if !strings.Contains(out, "field_1") {
		t.Errorf("describe should show the internal name field_1:\n%s", out)
	}
}

func TestDescribeOmitsInternalColumnWhenAllSame(t *testing.T) {
	// Schema where every column's display equals its internal name: no need
	// for an internal column — keep the output tight.
	e := &Executor{
		Bound: &BoundList{
			Columns: []string{"Title", "Count"},
			Schema: map[string]FieldInfo{
				"Title": {Name: "Title", DisplayName: "Title", Type: FieldText},
				"Count": {Name: "Count", DisplayName: "Count", Type: FieldNumber},
			},
		},
		Mode: "table",
		Out:  &bytes.Buffer{},
	}
	buf := &bytes.Buffer{}
	if err := e.Describe(buf, ""); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "internal") {
		t.Errorf("describe should NOT show an internal column when every display == internal:\n%s", out)
	}
}

func TestDescribeRejectsUnknownArg(t *testing.T) {
	e := describeTestExecutor(false)
	err := e.Describe(&bytes.Buffer{}, "verbose")
	if err == nil {
		t.Fatal("expected error for unknown describe argument")
	}
	if !strings.Contains(err.Error(), "unknown argument") {
		t.Errorf("error should mention unknown argument: %v", err)
	}
}

func TestBuildAliasMapSkipsIdentityAndEmpty(t *testing.T) {
	schema := map[string]FieldInfo{
		"Title":   {Name: "Title", DisplayName: "Title"},
		"field_1": {Name: "field_1", DisplayName: "vendor"},
		"field_2": {Name: "field_2", DisplayName: ""},
		"field_3": {Name: "field_3", DisplayName: "state"},
	}
	got := buildAliasMap(schema)
	if _, has := got["Title"]; has {
		t.Errorf("identity display name should not appear in alias map: %v", got)
	}
	if _, has := got["field_2"]; has {
		t.Errorf("empty display name should not appear in alias map: %v", got)
	}
	if v, ok := got["vendor"]; !ok || len(v) != 1 || v[0] != "field_1" {
		t.Errorf("vendor → %v, want [field_1]", v)
	}
	if v, ok := got["state"]; !ok || len(v) != 1 || v[0] != "field_3" {
		t.Errorf("state → %v, want [field_3]", v)
	}
}

func TestBuildAliasMapPreservesDuplicates(t *testing.T) {
	// Two columns with the same display name: the canonicalizer needs to
	// know both internal targets to report ambiguity.
	schema := map[string]FieldInfo{
		"field_1": {Name: "field_1", DisplayName: "state"},
		"field_2": {Name: "field_2", DisplayName: "state"},
	}
	got := buildAliasMap(schema)
	if len(got["state"]) != 2 {
		t.Errorf("state should have both internal names, got %v", got["state"])
	}
}
