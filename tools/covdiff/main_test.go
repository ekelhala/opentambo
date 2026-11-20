package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDiffHunks(t *testing.T) {
	diff := []byte(`diff --git a/internal/desk/app.go b/internal/desk/app.go
--- a/internal/desk/app.go
+++ b/internal/desk/app.go
@@ -10,0 +11,3 @@ func f() {
+line a
+line b
+line c
@@ -20 +22 @@ func g() {
-one
+two
diff --git a/internal/desk/app_test.go b/internal/desk/app_test.go
--- a/internal/desk/app_test.go
+++ b/internal/desk/app_test.go
@@ -1,0 +2,1 @@
+test line
diff --git a/internal/desk/api/gen.go b/internal/desk/api/gen.go
--- a/internal/desk/api/gen.go
+++ b/internal/desk/api/gen.go
@@ -1,0 +2,1 @@
+generated line
`)
	got := ParseDiffHunks(diff)
	if lines := got["internal/desk/app.go"]; len(lines) != 4 || !lines[11] || !lines[12] || !lines[13] || !lines[22] {
		t.Errorf("app.go changed lines = %v, want {11,12,13,22}", lines)
	}
	if _, ok := got["internal/desk/app_test.go"]; ok {
		t.Errorf("test files must not participate in the changed-line gate")
	}
	if _, ok := got["internal/desk/api/gen.go"]; ok {
		t.Errorf("generated wire types must not participate")
	}
}

func TestParseDiffHunksDeletionOnly(t *testing.T) {
	got := ParseDiffHunks([]byte("--- a/x/y.go\n+++ b/x/y.go\n@@ -5,2 +4,0 @@\n-gone\n-gone\n"))
	if len(got["x/y.go"]) != 0 {
		t.Errorf("pure deletion must add no lines, got %v", got["x/y.go"])
	}
}

func TestParseProfileFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.out")
	body := "mode: atomic\ngithub.com/ekelhala/opentambo/internal/desk/app.go:11.30,13.2 2 1\ngithub.com/ekelhala/opentambo/internal/desk/app.go:20.1,22.2 1 0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	blocks, err := ParseProfileFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].File != "internal/desk/app.go" || blocks[0].StartLine != 11 || blocks[0].EndLine != 13 || blocks[0].Count != 1 {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	if blocks[1].Count != 0 {
		t.Errorf("block 1 count = %d, want 0", blocks[1].Count)
	}
}

func TestEvaluateCountsOnlyExecutableChangedLines(t *testing.T) {
	blocks := []Block{
		{File: "a/b.go", StartLine: 10, EndLine: 12, Count: 3},
		{File: "a/b.go", StartLine: 20, EndLine: 21, Count: 0},
	}
	changed := FileLines{"a/b.go": {10: true, 12: true, 15: true, 20: true}}
	report := Evaluate(changed, blocks)
	if report.Total != 3 {
		t.Errorf("total executable changed lines = %d, want 3 (line 15 is outside any block)", report.Total)
	}
	if report.Covered != 2 {
		t.Errorf("covered = %d, want 2", report.Covered)
	}
	if len(report.Files) != 1 || len(report.Files[0].Uncovered) != 1 || report.Files[0].Uncovered[0] != 20 {
		t.Errorf("file report = %+v, want uncovered [20]", report.Files[0])
	}
}

func TestEvaluateNoProfileBlocksIsNotExecutable(t *testing.T) {
	report := Evaluate(FileLines{"a/b.go": {5: true}}, nil)
	if report.Total != 0 {
		t.Errorf("total = %d, want 0 for a file without statement blocks", report.Total)
	}
}

func TestEligible(t *testing.T) {
	cases := map[string]bool{
		"internal/desk/app.go":                 true,
		"tools/covdiff/main.go":                true,
		"internal/desk/app_test.go":            false,
		"test/integration/lifecycle_test.go":   false,
		"internal/desk/api/gen.go":             false,
		"docs/testing.md":                      false,
		"ui/src/App.tsx":                       false,
		"test/integration/harness/helpers.go":  false,
	}
	for path, want := range cases {
		if got := Eligible(path); got != want {
			t.Errorf("Eligible(%q) = %v, want %v", path, got, want)
		}
	}
}
