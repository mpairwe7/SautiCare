package main

import (
	"os/exec"
	"testing"
)

const mod = "sautihealth.com/sauti"

func pkg(path string, imports ...string) Package {
	return Package{ImportPath: mod + "/" + path, Imports: imports}
}

func rules(vs []Violation) map[string]int {
	m := map[string]int{}
	for _, v := range vs {
		m[v.Rule]++
	}
	return m
}

func TestR1ContextMayNotImportPlatform(t *testing.T) {
	vs := Check(mod, []Package{pkg("internal/incident", mod+"/internal/platform/pg/opstore")})
	if rules(vs)["R1"] != 1 {
		t.Fatalf("violations = %v, want one R1", vs)
	}
}

func TestR2UnlistedContextEdgeRejected(t *testing.T) {
	vs := Check(mod, []Package{
		pkg("internal/crypto", mod+"/internal/incident"),              // crypto may import only pii
		pkg("internal/intake", mod+"/internal/incident"),              // allowed
		pkg("internal/newthing/sub", mod+"/internal/pii"),             // unregistered context
		pkg("internal/incident/internal/x", mod+"/internal/incident"), // same context: fine
	})
	if got := rules(vs)["R2"]; got != 2 {
		t.Fatalf("violations = %v, want two R2", vs)
	}
}

func TestR3NetOnlyInPlatform(t *testing.T) {
	vs := Check(mod, []Package{
		pkg("internal/audit", "net/http"),
		pkg("internal/incident", "github.com/jackc/pgx/v5"),
		pkg("db", "database/sql"),
		pkg("internal/platform/egress", "net/http"),
		pkg("cmd/intake-gw", "net/http"),
		pkg("tools/amendgate", "os/exec"),
	})
	if got := rules(vs)["R3"]; got != 3 {
		t.Fatalf("violations = %v, want three R3", vs)
	}
}

func TestR4OnlyPseudonymiserJoinsStores(t *testing.T) {
	op := mod + "/internal/platform/pg/opstore"
	an := mod + "/internal/platform/pg/analyticstore"
	vs := Check(mod, []Package{
		{ImportPath: mod + "/cmd/pseudonymiser", Deps: []string{op, an}},
		{ImportPath: mod + "/cmd/console-api", Deps: []string{op, an}},
		{ImportPath: mod + "/cmd/intake-gw", Deps: []string{op}},
	})
	if got := rules(vs)["R4"]; got != 1 || vs[0].Package != mod+"/cmd/console-api" {
		t.Fatalf("violations = %v, want one R4 for console-api", vs)
	}
}

// The real module must satisfy every rule.
func TestRealModulePasses(t *testing.T) {
	out, err := exec.Command("go", "list", "-json", mod+"/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	pkgs, err := Decode(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) < 10 {
		t.Fatalf("decoded only %d packages", len(pkgs))
	}
	if vs := Check(mod, pkgs); len(vs) != 0 {
		t.Fatalf("module violates boundaries: %v", vs)
	}
}
