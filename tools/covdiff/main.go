// Command covdiff computes statement coverage for *changed executable Go
// lines* between a base ref and the working tree (issue #19). It cross-
// references `git diff -U0` hunk headers with a Go coverage profile
// (`go test -coverprofile`) and fails when changed executable lines fall
// below the threshold. When no eligible executable line changed, the run
// reports N/A and passes — a docs-only delta must not be blocked by a
// meaningless 0/0.
//
// Exclusions (documented, reviewable): *_test.go files, the build-tagged
// real-backend suite under test/integration (verified by executing it, not
// by unit instrumentation), generated wire types, and build output.
//
// Usage: covdiff -base <ref> [-threshold 90] coverage.out
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const modulePrefix = "github.com/ekelhala/opentambo/"

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "covdiff:", err)
		os.Exit(1)
	}
}

// runCLI is the testable body of the command: parse flags, evaluate the
// profile, and report. It returns an error instead of exiting.
func runCLI(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("covdiff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "origin/main", "git base ref for the changed-line comparison")
	threshold := fs.Float64("threshold", 90, "required coverage percentage of changed executable lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	profiles := fs.Args()
	if len(profiles) == 0 {
		profiles = []string{"coverage.out"}
	}
	return run(*base, profiles, *threshold, stdout)
}

// run evaluates the gate against one or more coverage profiles. A changed
// line counts as covered when any profile shows a block executing over it:
// that is how the unit profile (coverage.out) and the instrumented
// real-binary integration profile (coverage.integration.out) combine —
// paths only a real process reaches, like main(), are proven honestly,
// not exempted.
func run(base string, profiles []string, threshold float64, stdout io.Writer) error {
	changed, err := ChangedLines(base)
	if err != nil {
		return err
	}
	var blocks []Block
	for _, profile := range profiles {
		parsed, err := ParseProfileFile(profile)
		if err != nil {
			return err
		}
		blocks = append(blocks, parsed...)
	}
	report := Evaluate(changed, blocks)
	// Report writes are best-effort: a closed stdout must not turn a
	// coverage verdict into a different error.
	if report.Total == 0 {
		_, _ = fmt.Fprintln(stdout, "changed-coverage: N/A (no changed executable Go lines against", base+")")
		return nil
	}
	pct := 100 * float64(report.Covered) / float64(report.Total)
	_, _ = fmt.Fprintf(stdout, "changed-coverage: %d/%d executable changed lines (%.1f%%) vs %s\n", report.Covered, report.Total, pct, base)
	for _, f := range report.Files {
		_, _ = fmt.Fprintf(stdout, "  %s: %d/%d\n", f.File, f.Covered, f.Total)
		for _, line := range f.Uncovered {
			_, _ = fmt.Fprintf(stdout, "    uncovered changed line %d\n", line)
		}
	}
	if pct < threshold {
		return fmt.Errorf("changed executable lines at %.1f%% are below the %.0f%% gate", pct, threshold)
	}
	return nil
}

// FileLines maps a repo-relative Go file path to the set of added line
// numbers in the working tree.
type FileLines map[string]map[int]bool

// changedHeader matches `+++ b/<path>` diff headers.
var changedHeader = regexp.MustCompile(`^\+\+\+ b/(.+)$`)

// changedHunk matches `@@ -a,b +c,d @@` hunk headers (d omitted means 1).
var changedHunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// ChangedLines returns the added lines per Go file for `git diff base`.
func ChangedLines(base string) (FileLines, error) {
	cmd := exec.Command("git", "diff", "-U0", "--no-color", base, "--", "*.go")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", base, err)
	}
	return ParseDiffHunks(out), nil
}

// ParseDiffHunks extracts added line numbers per file from unified-diff
// bytes, keeping only eligible non-test Go source files.
func ParseDiffHunks(diff []byte) FileLines {
	lines := FileLines{}
	var file string
	scanner := bufio.NewScanner(strings.NewReader(string(diff)))
	for scanner.Scan() {
		text := scanner.Text()
		if m := changedHeader.FindStringSubmatch(text); m != nil {
			file = ""
			if m[1] != "/dev/null" && Eligible(m[1]) {
				file = m[1]
			}
			continue
		}
		if file == "" {
			continue
		}
		if m := changedHunk.FindStringSubmatch(text); m != nil {
			recordAddedLines(lines, file, m)
		}
	}
	return lines
}

// recordAddedLines adds the +side line range of one hunk header to the set.
func recordAddedLines(lines FileLines, file string, hunk []string) {
	start, err := strconv.Atoi(hunk[1])
	if err != nil {
		return
	}
	count := 1
	if hunk[2] != "" {
		if count, err = strconv.Atoi(hunk[2]); err != nil {
			return
		}
	}
	if count == 0 { // pure deletion
		return
	}
	if lines[file] == nil {
		lines[file] = map[int]bool{}
	}
	for i := start; i < start+count; i++ {
		lines[file][i] = true
	}
}

// Eligible reports whether a repo-relative path participates in the
// changed-line gate.
func Eligible(path string) bool {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return false
	}
	switch {
	case strings.HasPrefix(path, "test/integration/"): // executed as a suite
		return false
	case strings.HasPrefix(path, "internal/desk/api/"): // generated wire types
		return false
	case strings.HasPrefix(path, "tools/tsgen/"):
		return false
	}
	return true
}

// Block is one coverage profile entry: an inclusive line range in a file
// with N statements and an execution count.
type Block struct {
	File      string
	StartLine int
	EndLine   int
	Count     int
}

// ParseProfileFile reads a Go coverage profile ("mode: …" header followed
// by `file:start.col,end.col numstmt count` lines) into blocks with
// repo-relative paths.
func ParseProfileFile(path string) ([]Block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var blocks []Block
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "mode:") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 3 {
			continue
		}
		loc := strings.SplitN(fields[0], ":", 2)
		if len(loc) != 2 {
			continue
		}
		file := strings.TrimPrefix(loc[0], modulePrefix)
		span := strings.Split(loc[1], ",")
		if len(span) != 2 {
			continue
		}
		startLine, err := strconv.Atoi(strings.SplitN(span[0], ".", 2)[0])
		if err != nil {
			continue
		}
		endLine, err := strconv.Atoi(strings.SplitN(span[1], ".", 2)[0])
		if err != nil {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		blocks = append(blocks, Block{File: filepath.ToSlash(file), StartLine: startLine, EndLine: endLine, Count: count})
	}
	return blocks, scanner.Err()
}

// FileReport is the per-file changed-line result.
type FileReport struct {
	File      string
	Total     int
	Covered   int
	Uncovered []int
}

// Report is the whole diff-coverage evaluation.
type Report struct {
	Total   int
	Covered int
	Files   []FileReport
}

// lineStatus is the coverage verdict for one changed line.
type lineStatus int

const (
	lineNotExecutable lineStatus = iota // outside any statement block
	lineCovered
	lineUncovered
)

// Evaluate determines, for every changed line, whether at least one
// covering profile block was executed. Lines outside any block (comments,
// blanks, import lines) are not executable and do not count.
func Evaluate(changed FileLines, blocks []Block) Report {
	byFile := map[string][]Block{}
	for _, b := range blocks {
		byFile[b.File] = append(byFile[b.File], b)
	}
	names := make([]string, 0, len(changed))
	for file := range changed {
		names = append(names, file)
	}
	sort.Strings(names)
	var report Report
	for _, file := range names {
		fr, ok := evaluateFile(file, changed[file], byFile[file])
		if !ok {
			continue
		}
		report.Total += fr.Total
		report.Covered += fr.Covered
		report.Files = append(report.Files, fr)
	}
	return report
}

// evaluateFile scores the changed lines of one file. ok is false when the
// file has no statement blocks or no executable change (removed file,
// docs-only Go change).
func evaluateFile(file string, lines map[int]bool, fileBlocks []Block) (FileReport, bool) {
	if len(fileBlocks) == 0 {
		return FileReport{}, false
	}
	numbers := make([]int, 0, len(lines))
	for n := range lines {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	fr := FileReport{File: file}
	for _, n := range numbers {
		switch lineStatusOf(n, fileBlocks) {
		case lineNotExecutable:
			continue
		case lineCovered:
			fr.Total++
			fr.Covered++
		case lineUncovered:
			fr.Total++
			fr.Uncovered = append(fr.Uncovered, n)
		}
	}
	return fr, fr.Total > 0
}

// lineStatusOf classifies one line against the file's statement blocks.
func lineStatusOf(line int, blocks []Block) lineStatus {
	status := lineNotExecutable
	for _, b := range blocks {
		if line < b.StartLine || line > b.EndLine {
			continue
		}
		status = lineUncovered
		if b.Count > 0 {
			return lineCovered
		}
	}
	return status
}
