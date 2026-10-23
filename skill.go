// Author: David M. Anderson
// Built with AI assistance (Claude, Anthropic)

package xql

// Package xql exists for one reason: //go:embed cannot contain ".." or escape
// its own package directory, so a package under cmd/ cannot reach skills/ at
// the repo root. Moving the canonical files would break the GitHub-browsable
// path, and symlinking would break the raw-URL curl fallback, because
// raw.githubusercontent serves a symlink as the text of its target. A
// root-level package that cmd/xql imports is the way out.
//
// --install-skill / --uninstall-skill: write the repo's Claude Code skill into
// the user's personal skills directory.
//
// The skill is compiled into the binary rather than shipped as a package data
// file, because the binary is the only artifact present on every install path.
// apt, Homebrew, winget, `go install`, the curl one-liner, a prebuilt GitHub
// binary and a source build all produce the binary; `go install` in particular
// ships no data files at all. Embedding is what makes one instruction —
// `xql --install-skill` — true everywhere.
//
// The contract this implements is fleet-wide and pinned in
// ~/notes/skill_fleet_triage.md, so the Go and Rust tools cannot drift.

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// skillTool is the canonical command for this skill, named in the revision
// stamp. Deliberately a constant rather than the invoked name: a repo can ship
// several binaries over one skill — the five xfiles tools, for instance — and
// if the stamp named whichever one ran, the bytes would differ per binary and
// the idempotence check would report an update every time they alternated.
const skillTool = "xql"

// skillName is the directory under ~/.claude/skills/, matching skills/<name>/
// in the repo so a manual `cp -r` needs no rename either.
const skillName = "xql"

// skillFS carries the whole skills/ tree. Unlike Rust's include_str!, the Go
// embed has a directory form, so a page added to the skill ships without any
// list here needing to be updated.
//
//go:embed skills
var skillFS embed.FS

// invokedAs is the command the user actually typed, for message prefixes.
// Where one skill covers several binaries a diagnostic should name the one that
// was run; for a single-binary repo this is just skillTool.
func invokedAs() string {
	if len(os.Args) == 0 {
		return skillTool
	}
	name := filepath.Base(os.Args[0])
	name = strings.TrimSuffix(name, ".exe")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return skillTool
	}
	return name
}

// skillDest is ~/.claude/skills/<name>, resolved from the environment directly
// rather than through a library — it is five lines, and a dependency would be
// carried by every install of the tool to serve one flag.
func skillDest() (string, error) {
	var home string
	if runtime.GOOS == "windows" {
		home = os.Getenv("USERPROFILE")
	} else {
		home = os.Getenv("HOME")
	}
	if home == "" {
		v := "HOME"
		if runtime.GOOS == "windows" {
			v = "USERPROFILE"
		}
		return "", fmt.Errorf("cannot find your home directory: %s is not set", v)
	}
	return filepath.Join(home, ".claude", "skills", skillName), nil
}

// stampSkill inserts the revision stamp directly after the YAML frontmatter.
//
// Outside the frontmatter, never inside it: the description in that block is
// what decides whether the skill fires at all, and a stamp that broke the parse
// would take the whole skill down rather than merely misreport itself.
func stampSkill(body, revision string) string {
	note := fmt.Sprintf(
		"> This skill is revision %s. If `%s -V` reports a different skill\n"+
			"> revision, the skill is stale — run `%s --install-skill` to refresh it.\n",
		revision, skillTool, skillTool)
	const fence = "---\n"
	if rest, ok := strings.CutPrefix(body, fence); ok {
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			split := len(fence) + i + len(fence) + 1
			return body[:split] + "\n" + note + body[split:]
		}
	}
	// No frontmatter to sit under — still stamp it, at the top.
	return note + "\n" + body
}

// stampedLabel reads back what an earlier install stamped: the revision, or
// for a stamp from before revisions, the tool and version it named.
func stampedLabel(body string) string {
	old := fmt.Sprintf("> This skill documents %s ", skillTool)
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(line, "> This skill is revision "); ok {
			return firstWord(rest)
		}
		if rest, ok := strings.CutPrefix(line, old); ok {
			if v := firstWord(rest); v != "" {
				return skillTool + " " + v
			}
		}
	}
	return ""
}

func firstWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimSuffix(f[0], ".")
}

// embeddedSkill is the skill as compiled in, unstamped, keyed by filename.
func embeddedSkill() (map[string]string, error) {
	out := map[string]string{}
	dir := filepath.ToSlash(filepath.Join("skills", skillName))
	entries, err := fs.ReadDir(skillFS, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := skillFS.ReadFile(dir + "/" + e.Name())
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

// SkillRevision is the embedded skill's revision, which the stamp and -V both
// carry.
func SkillRevision() string {
	files, err := embeddedSkill()
	if err != nil {
		panic(err) // the files are compiled in, so this cannot fail at run time
	}
	return revisionOf(files)
}

// revisionOf is the first eight hex digits of SHA-256 over the files sorted by
// path, each fed as its path, a NUL, its byte length in decimal, a NUL, and its
// bytes. The Rust tools hash the same form, so equal skill text gives an equal
// revision across the fleet.
func revisionOf(files map[string]string) string {
	h := sha256.New()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(h, "%s\x00%d\x00%s", name, len(files[name]), files[name])
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:4])
}

// skillPayload is the exact bytes an install would write, keyed by filename.
func skillPayload(revision string) (map[string]string, error) {
	out, err := embeddedSkill()
	if err != nil {
		return nil, err
	}
	out["SKILL.md"] = stampSkill(out["SKILL.md"], revision)
	return out, nil
}

// InstallSkill handles --install-skill. Returns the process exit code.
func InstallSkill() int {
	me := invokedAs()
	revision := SkillRevision()
	dest, err := skillDest()
	if err != nil {
		return skillFail(err.Error())
	}

	// A symlink here is a developer's sync-skills pointing the installed skill
	// straight at a working tree, which tracks edits a copy cannot. Overwriting
	// it would silently disconnect the two, so report and stop.
	if target, err := os.Readlink(dest); err == nil {
		fmt.Printf("%s: %s is a symlink to %s\n", me, dest, target)
		fmt.Printf("%s: leaving it alone — a link tracks its source directly, "+
			"which is what you want on a machine that edits the skill.\n", me)
		return 0
	}

	files, err := skillPayload(revision)
	if err != nil {
		return skillFail(fmt.Sprintf("cannot read the embedded skill: %v", err))
	}

	previous, _ := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	unchanged := true
	for name, text := range files {
		onDisk, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(onDisk) != text {
			unchanged = false
			break
		}
	}
	if unchanged {
		fmt.Printf("%s: skill already current at %s (skill %s)\n", me, dest, revision)
		return 0
	}

	_, statErr := os.Stat(dest)
	fresh := os.IsNotExist(statErr)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return skillFail(fmt.Sprintf("cannot create %s: %v", dest, err))
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dest, name), []byte(text), 0o644); err != nil {
			return skillFail(fmt.Sprintf("cannot write %s: %v", filepath.Join(dest, name), err))
		}
	}

	if fresh {
		fmt.Printf("%s: installed skill to %s (skill %s)\n", me, dest, revision)
		fmt.Printf("%s: restart Claude Code to pick it up.\n", me)
		return 0
	}
	if old := stampedLabel(string(previous)); old != "" && old != revision {
		fmt.Printf("%s: updated skill at %s (%s → %s)\n", me, dest, old, revision)
	} else {
		fmt.Printf("%s: updated skill at %s (skill %s)\n", me, dest, revision)
	}
	fmt.Printf("%s: restart Claude Code to pick up the change.\n", me)
	return 0
}

// UninstallSkill handles --uninstall-skill. Returns the process exit code.
//
// Unlike install, this does remove a symlink. Refusing would strand a dangling
// link when the repo goes away, and sync-skills recreates a link in a second —
// so nothing is lost by removing one, while overwriting a link on install would
// lose the connection to the tree it points at.
func UninstallSkill() int {
	me := invokedAs()
	dest, err := skillDest()
	if err != nil {
		return skillFail(err.Error())
	}
	target, linkErr := os.Readlink(dest)
	if linkErr != nil {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			fmt.Printf("%s: no skill installed at %s\n", me, dest)
			return 0
		}
		if err := os.RemoveAll(dest); err != nil {
			return skillFail(fmt.Sprintf("cannot remove %s: %v", dest, err))
		}
		fmt.Printf("%s: removed the skill at %s\n", me, dest)
		return 0
	}
	if err := os.Remove(dest); err != nil {
		return skillFail(fmt.Sprintf("cannot remove %s: %v", dest, err))
	}
	fmt.Printf("%s: removed the symlink at %s (it pointed at %s)\n", me, dest, target)
	return 0
}

func skillFail(message string) int {
	fmt.Fprintf(os.Stderr, "%s: %s\n", invokedAs(), message)
	return 1
}
