// Author: David M. Anderson
// Built with AI assistance (Claude, Anthropic)

package xql

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The frontmatter is what decides whether the skill fires at all, so a stamp
// that landed inside it would take the whole skill down rather than merely
// misreport itself.
func TestStampSitsUnderTheFrontmatterNotInsideIt(t *testing.T) {
	body := "---\nname: xql\ndescription: x\n---\n\n# xql\n"
	out := stampSkill(body, "0123abcd")

	end := strings.Index(out, "\n---\n") + len("\n---\n")
	if strings.Contains(out[:end], "This skill is revision") {
		t.Fatalf("the stamp landed inside the YAML block:\n%s", out)
	}
	if !strings.Contains(out[end:], "This skill is revision 0123abcd.") {
		t.Fatalf("the stamp is missing from the body:\n%s", out)
	}
	if !strings.HasSuffix(out, "# xql\n") {
		t.Fatalf("the body did not survive stamping:\n%s", out)
	}
}

func TestStampRoundTripsThroughTheReader(t *testing.T) {
	stamped := stampSkill("---\nname: xql\n---\n\nbody\n", "1a2b3c4d")
	if got := stampedLabel(stamped); got != "1a2b3c4d" {
		t.Fatalf("stampedLabel = %q, want 1a2b3c4d", got)
	}
}

// A skill installed before revisions carries a version stamp, and the first
// update has to name it.
func TestVersionStampReadsBackAsToolAndVersion(t *testing.T) {
	old := "---\nname: xql\n---\n\n> This skill documents " + skillTool + " 0.5.1. If `x -V` " +
		"reports a different\n> version, the skill is stale.\n"
	if got, want := stampedLabel(old), skillTool+" 0.5.1"; got != want {
		t.Fatalf("stampedLabel = %q, want %q", got, want)
	}
}

func TestUnstampedSkillReadsBackAsNothing(t *testing.T) {
	if got := stampedLabel("---\nname: xql\n---\n\nbody\n"); got != "" {
		t.Fatalf("stampedLabel = %q, want empty", got)
	}
}

// The same fixture and answer are in the Rust tools' tests, which is what
// holds the two languages to one canonical form.
func TestRevisionMatchesTheRustTools(t *testing.T) {
	got := revisionOf(map[string]string{"reference.md": "b\n", "SKILL.md": "a\n"})
	if got != "0cce3525" {
		t.Fatalf("revisionOf = %q, want 0cce3525", got)
	}
}

func TestStampCarriesTheRevisionVersionPrints(t *testing.T) {
	files, err := skillPayload(SkillRevision())
	if err != nil {
		t.Fatal(err)
	}
	if got := stampedLabel(files["SKILL.md"]); got != SkillRevision() {
		t.Fatalf("the stamp names %q, -V names %q", got, SkillRevision())
	}
}

// The real SKILL.md must survive stamping with its frontmatter intact.
func TestShippedSkillKeepsItsFrontmatter(t *testing.T) {
	files, err := skillPayload("0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := files["SKILL.md"]
	if !ok {
		t.Fatal("SKILL.md is missing from the payload")
	}
	if !strings.HasPrefix(got, "---\nname: "+skillName+"\n") {
		t.Fatalf("frontmatter did not survive stamping:\n%.120s", got)
	}
	raw, err := skillFS.ReadFile("skills/" + skillName + "/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "\n---\n") != strings.Count(string(raw), "\n---\n") {
		t.Fatal("stamping changed the number of YAML fences")
	}
}

// The embed must actually carry the files. A wrong //go:embed path compiles
// happily and installs an empty directory.
func TestEmbedCarriesEverySkillFile(t *testing.T) {
	dir := "skills/" + skillName
	entries, err := fs.ReadDir(skillFS, dir)
	if err != nil {
		t.Fatalf("the embedded skill directory is unreadable: %v", err)
	}
	var embedded []string
	for _, e := range entries {
		if !e.IsDir() {
			embedded = append(embedded, e.Name())
		}
	}
	if len(embedded) == 0 {
		t.Fatal("the embed carried no files")
	}
	onDisk, err := filepath.Glob(filepath.Join("skills", skillName, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) != len(onDisk) {
		t.Fatalf("embed has %v, the repo ships %v", embedded, onDisk)
	}
	if _, ok := findFile(embedded, "SKILL.md"); !ok {
		t.Fatalf("SKILL.md is not among the embedded files: %v", embedded)
	}
}

func findFile(names []string, want string) (string, bool) {
	for _, n := range names {
		if n == want {
			return n, true
		}
	}
	return "", false
}
