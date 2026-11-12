// Command archcheck enforces the modular monolith's boundaries in CI
// (Build Prompt §8; spec §4.1; ADR-0002):
//
//	R1  a bounded context never imports internal/platform (adapters)
//	R2  a context imports only the contexts listed in rules.go
//	R3  network, process and database packages appear only in
//	    internal/platform, cmd, tests and tools
//	R4  only cmd/pseudonymiser links both the operational and analytic stores
//
// Usage: go run ./tools/archcheck
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

const module = "sautihealth.com/sauti"

func main() {
	out, err := exec.CommandContext(context.Background(), "go", "list", "-json", module+"/...").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck: go list:", err)
		os.Exit(2)
	}
	pkgs, err := Decode(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	vs := Check(module, pkgs)
	for _, v := range vs {
		fmt.Printf("%s: %s imports %q\n", v.Rule, v.Package, v.Import)
	}
	fmt.Printf("archcheck: %d packages, %d violations\n", len(pkgs), len(vs))
	if len(vs) > 0 {
		os.Exit(1)
	}
}
