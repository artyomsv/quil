package gitworktree

import (
	"path/filepath"
	"strings"
	"testing"
)

// The name reaches `git worktree add -b <branch>` as argv AND becomes a path
// segment. Both grammars are checked, because a name legal as one can be
// dangerous as the other — a leading dash is a flag, and a name that walks up
// a directory puts a checkout somewhere the user never asked for.
func TestValidateBranch(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"ordinary name", "feat-x", false},
		{"slashes are legal in a ref", "feat/x", false},
		{"digits and dots inside", "release-1.2.3", false},
		{"underscore", "feat_x", false},
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"leading dash reads as a flag", "-b", true},
		{"double dot", "feat/../x", true},
		{"bare double dot", "..", true},
		{"space", "feat x", true},
		{"tilde", "feat~1", true},
		{"caret", "feat^", true},
		{"colon", "feat:x", true},
		{"question mark", "feat?", true},
		{"asterisk", "feat*", true},
		{"open bracket", "feat[x", true},
		{"backslash", "feat\\x", true},
		{"lock suffix", "feat.lock", true},
		{"trailing slash", "feat/", true},
		{"leading slash", "/feat", true},
		{"double slash", "feat//x", true},
		{"lone dot", ".", true},
		{"leading dot segment", ".hidden", true},
		{"control character", "feat\x01x", true},
		{"newline", "feat\nx", true},
		{"del", "feat\x7fx", true},
		{"absolute windows path", "C:/evil", true},
		{"at-brace", "feat@{1}", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBranch(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBranch(%q) err = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

// The sibling location keeps the repo tree clean: no .gitignore entry, and no
// tool that walks the tree — ripgrep, a file watcher, the agent itself — finds
// a second full checkout nested inside the first.
func TestDerivePath_SiblingDirectory(t *testing.T) {
	got := DerivePath(filepath.Join("/home", "u", "quil"), "feat/x")
	want := filepath.Join("/home", "u", "quil-worktrees", "feat-x")
	if got != want {
		t.Errorf("DerivePath = %q, want %q", got, want)
	}
}

// Separators are SWAPPED, not rejected: feat/x is an ordinary branch name and
// must not produce a nested directory under the worktrees parent, where two
// branches could then collide on an intermediate segment.
func TestDerivePath_FlattensSeparators(t *testing.T) {
	repo := filepath.Join("/home", "u", "repo")
	got := DerivePath(repo, "feat/deep/nested")
	parent := filepath.Join("/home", "u", "repo-worktrees")

	rel, err := filepath.Rel(parent, got)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if strings.ContainsRune(rel, filepath.Separator) {
		t.Errorf("DerivePath = %q, want a single flat segment under %q (got %q)", got, parent, rel)
	}
	if rel != "feat-deep-nested" {
		t.Errorf("segment = %q, want \"feat-deep-nested\"", rel)
	}
}

// Every name ValidateBranch accepts must derive a path that stays inside the
// worktrees parent. This is the property; the table above only samples it. An
// escape here puts a checkout somewhere the user never named.
func TestDerivePath_CannotEscapeTheParent(t *testing.T) {
	repo := filepath.Join("/home", "u", "repo")
	parent := filepath.Join("/home", "u", "repo-worktrees")
	for _, branch := range []string{"feat-x", "feat/x", "release-1.2.3", "a", "x/y/z", "feat_x"} {
		if err := ValidateBranch(branch); err != nil {
			t.Fatalf("fixture %q is not accepted: %v", branch, err)
		}
		got := DerivePath(repo, branch)
		rel, err := filepath.Rel(parent, got)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			t.Errorf("DerivePath(%q) = %q escapes %q (rel=%q, err=%v)", branch, got, parent, rel, err)
		}
	}
}

// Two branches that flatten to the same segment would land on one directory,
// and git refuses the second — a fact worth surfacing at the point the name is
// chosen rather than as a git error later.
func TestDerivePath_FlatteningCanCollide(t *testing.T) {
	repo := filepath.Join("/home", "u", "repo")
	if DerivePath(repo, "feat/x") != DerivePath(repo, "feat-x") {
		t.Skip("flattening no longer collides; the Add error path covers this instead")
	}
}

// git applies its ref rules to every slash-separated COMPONENT, not just the
// whole name. Checking only the whole name let these through to cost a permit,
// a single-flight slot and a subprocess before git refused them — defeating
// the fail-fast property this function exists for.
func TestValidateBranch_PerComponentRules(t *testing.T) {
	for _, branch := range []string{
		"feat/.hidden",  // component starts with a dot
		"feat/x.lock",   // component ends in .lock
		"a/b/.c",        // deeper component
		"feat.",         // trailing dot
		"@",             // the lone @ ref
		strings.Repeat("x", maxBranchLen+1), // unbounded name
	} {
		if err := ValidateBranch(branch); err == nil {
			t.Errorf("ValidateBranch(%.40q) accepted a name git refuses", branch)
		}
	}
}

// The bound is on the whole name and must not reject ordinary ones.
func TestValidateBranch_AcceptsAtTheLengthLimit(t *testing.T) {
	if err := ValidateBranch(strings.Repeat("x", maxBranchLen)); err != nil {
		t.Errorf("a %d-character name was refused: %v", maxBranchLen, err)
	}
}
