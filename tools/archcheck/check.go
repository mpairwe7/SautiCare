package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
)

// Package is the subset of `go list -json` the checker needs.
type Package struct {
	ImportPath string
	Imports    []string
	Deps       []string
}

// Violation is one broken boundary rule.
type Violation struct {
	Package, Import, Rule string
}

// Decode reads the concatenated JSON objects printed by `go list -json`.
func Decode(b []byte) ([]Package, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var out []Package
	for {
		var p Package
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
}

// contextOf returns the bounded context of a module-relative path
// ("internal/incident/x" → "incident"), or "" if it is not in one.
func contextOf(rel string) string {
	rest, ok := strings.CutPrefix(rel, "internal/")
	if !ok {
		return ""
	}
	ctx, _, _ := strings.Cut(rest, "/")
	if ctx == "platform" {
		return ""
	}
	return ctx
}

func isIO(imp string) bool {
	if slices.Contains(netImports, imp) {
		return true
	}
	for _, p := range netPrefixes {
		if strings.HasPrefix(imp, p) {
			return true
		}
	}
	return false
}

func ioAllowed(rel string) bool {
	for _, loc := range ioLocations {
		if strings.HasPrefix(rel+"/", loc) {
			return true
		}
	}
	return false
}

// Check applies rules R1–R4 to the module's packages.
func Check(module string, pkgs []Package) []Violation {
	prefix := module + "/"
	var out []Violation
	for _, p := range pkgs {
		rel := strings.TrimPrefix(p.ImportPath, prefix)
		ctx := contextOf(rel)
		_, known := Allowed[ctx]
		if ctx != "" && !known {
			// Report an unregistered context once, not once per import.
			out = append(out, Violation{p.ImportPath, "", "R2"})
		}
		for _, imp := range p.Imports {
			if impRel, inModule := strings.CutPrefix(imp, prefix); inModule {
				if ctx == "" {
					continue
				}
				if strings.HasPrefix(impRel, "internal/platform/") || impRel == "internal/platform" {
					out = append(out, Violation{p.ImportPath, imp, "R1"})
					continue
				}
				if other := contextOf(impRel); known && other != "" && other != ctx && !slices.Contains(Allowed[ctx], other) {
					out = append(out, Violation{p.ImportPath, imp, "R2"})
				}
				continue
			}
			if isIO(imp) && !ioAllowed(rel) {
				out = append(out, Violation{p.ImportPath, imp, "R3"})
			}
		}
		if rel != pseudonymiser && slices.Contains(p.Deps, prefix+storeJoin[0]) && slices.Contains(p.Deps, prefix+storeJoin[1]) {
			out = append(out, Violation{p.ImportPath, storeJoin[1], "R4"})
		}
	}
	return out
}
