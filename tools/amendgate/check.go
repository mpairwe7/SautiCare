package main

import (
	"path"
	"strings"
	"time"
)

// DeletedMarker is the sha256 value an amendment uses to approve a deletion.
const DeletedMarker = "deleted"

// ChangedFile is a path changed between the base ref and HEAD.
type ChangedFile struct {
	Path    string
	SHA256  string
	Deleted bool
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
	if !windowActive {
		return nil
	}
	var out []Violation
	for _, c := range changed {
		if !gatedPath(gatedGlobs, c.Path) {
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
