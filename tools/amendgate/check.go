package main

import (
	"path"
	"strings"
	"time"
)

// DeletedMarker is the sha256 value an amendment uses to approve a deletion.
const DeletedMarker = "deleted"

// ChangedFile is a path changed between the base ref and HEAD. SHA256 is
// the hash of the content tracked in Git at HEAD (not of whatever a working
// tree path resolves to).
type ChangedFile struct {
	Path    string
	SHA256  string
	Deleted bool
	Symlink bool
}

// builtinGated is the protected minimum, applied whatever the gate list in
// the change says: the change under review cannot unfreeze itself
// (CodeRabbit PR #2). The gate list itself is gated.
var builtinGated = []string{"config/gated/**", "config/study.yaml", "triage-rules/**", "governance/gated-paths.txt"}

// effectiveGlobs is the union of the built-in minimum, the gate list at the
// base ref and the gate list in the change, so a change can only widen it.
func effectiveGlobs(base, head []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{builtinGated, base, head} {
		for _, g := range list {
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// Amendment is one approved protocol amendment (governance/amendments.yaml).
type Amendment struct {
	Reference  string        `yaml:"reference"`
	ApprovedOn string        `yaml:"approved_on"`
	Files      []AmendedFile `yaml:"files"`
}

// AmendedFile is the approved content of one gated file.
type AmendedFile struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

// Violation is a gated change with no matching amendment.
type Violation struct {
	Path, Reason string
}

// matches reports whether p is covered by glob ("dir/**" is a prefix match).
func matches(glob, p string) bool {
	if prefix, ok := strings.CutSuffix(glob, "/**"); ok {
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	ok, err := path.Match(glob, p)
	return err == nil && ok
}

func gatedPath(globs []string, p string) bool {
	for _, g := range globs {
		if matches(g, p) {
			return true
		}
	}
	return false
}

func validAmendment(a Amendment) bool {
	if strings.TrimSpace(a.Reference) == "" {
		return false
	}
	_, err := time.Parse(time.DateOnly, a.ApprovedOn)
	return err == nil
}

// Check returns one violation per gated change that no valid amendment
// approves at exactly the changed content. With the window inactive, gated
// paths may change freely (pre-study development).
func Check(windowActive bool, gatedGlobs []string, changed []ChangedFile, ams []Amendment) []Violation {
	var out []Violation
	for _, c := range changed {
		if !gatedPath(gatedGlobs, c.Path) {
			continue
		}
		// Always, window or not: a gated path must be a regular file, or the
		// effective configuration could change without the gated path changing.
		if c.Symlink {
			out = append(out, Violation{Path: c.Path, Reason: "gated path is a symbolic link; gated paths must be regular files"})
			continue
		}
		if !windowActive {
			continue
		}
		want := c.SHA256
		if c.Deleted {
			want = DeletedMarker
		}
		approved := false
		for _, a := range ams {
			if !validAmendment(a) {
				continue
			}
			for _, f := range a.Files {
				if f.Path == c.Path && f.SHA256 == want {
					approved = true
				}
			}
		}
		if !approved {
			out = append(out, Violation{Path: c.Path, Reason: "gated change during study window without a matching amendment_approved record"})
		}
	}
	return out
}
