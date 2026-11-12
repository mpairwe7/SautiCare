package main

import "testing"

var gated = []string{"config/gated/**", "triage-rules/**", "config/study.yaml"}

func TestCheckPassesWhenWindowInactive(t *testing.T) {
	changed := []ChangedFile{{Path: "config/gated/dispatch.yaml", SHA256: "aa"}}
	if v := Check(false, gated, changed, nil); len(v) != 0 {
		t.Fatalf("violations with window inactive: %v", v)
	}
}

func TestCheckFailsGatedChangeWithoutAmendment(t *testing.T) {
	changed := []ChangedFile{
		{Path: "config/gated/dispatch.yaml", SHA256: "aa"},
		{Path: "triage-rules/tool1.yaml", SHA256: "bb"},
	}
	v := Check(true, gated, changed, nil)
	if len(v) != 2 {
		t.Fatalf("got %d violations, want 2: %v", len(v), v)
	}
}

func TestCheckPassesWithMatchingAmendmentHash(t *testing.T) {
	changed := []ChangedFile{{Path: "config/gated/dispatch.yaml", SHA256: "aa"}}
	ams := []Amendment{{Reference: "MakSOM-REC-2026-123/A1", ApprovedOn: "2026-12-01",
		Files: []AmendedFile{{Path: "config/gated/dispatch.yaml", SHA256: "aa"}}}}
	if v := Check(true, gated, changed, ams); len(v) != 0 {
		t.Fatalf("violations despite matching amendment: %v", v)
	}
}

// An amendment approves specific content. Editing the file again after
// approval must fail, even though the path is listed.
func TestCheckFailsWhenHashDiffersFromAmendment(t *testing.T) {
	changed := []ChangedFile{{Path: "config/gated/dispatch.yaml", SHA256: "changed-again"}}
	ams := []Amendment{{Reference: "R1", ApprovedOn: "2026-12-01",
		Files: []AmendedFile{{Path: "config/gated/dispatch.yaml", SHA256: "aa"}}}}
	if v := Check(true, gated, changed, ams); len(v) != 1 {
		t.Fatalf("got %v, want 1 violation", v)
	}
}

func TestCheckRejectsIncompleteAmendmentRecord(t *testing.T) {
	changed := []ChangedFile{{Path: "config/gated/dispatch.yaml", SHA256: "aa"}}
	ams := []Amendment{{Reference: "", ApprovedOn: "not-a-date",
		Files: []AmendedFile{{Path: "config/gated/dispatch.yaml", SHA256: "aa"}}}}
	if v := Check(true, gated, changed, ams); len(v) == 0 {
		t.Fatal("amendment without reference/date accepted")
	}
}

func TestCheckIgnoresUngatedPaths(t *testing.T) {
	changed := []ChangedFile{{Path: "internal/incident/fold.go", SHA256: "x"}, {Path: "docs/adr/README.md", SHA256: "y"}}
	if v := Check(true, gated, changed, nil); len(v) != 0 {
		t.Fatalf("ungated paths flagged: %v", v)
	}
}

func TestCheckDeletionNeedsAmendment(t *testing.T) {
	changed := []ChangedFile{{Path: "config/gated/flags.yaml", Deleted: true}}
	if v := Check(true, gated, changed, nil); len(v) != 1 {
		t.Fatalf("deletion of gated file not flagged: %v", v)
	}
	ams := []Amendment{{Reference: "R2", ApprovedOn: "2026-12-02",
		Files: []AmendedFile{{Path: "config/gated/flags.yaml", SHA256: DeletedMarker}}}}
	if v := Check(true, gated, changed, ams); len(v) != 0 {
		t.Fatalf("approved deletion flagged: %v", v)
	}
}
