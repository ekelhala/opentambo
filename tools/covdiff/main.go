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
	base := flag.String("base", "origin/main", "git base ref for the changed-line comparison")
	threshold := flag.Float64("threshold", 90, "required coverage percentage of changed executable lines")
	flag.Parse()
	profile := flag.Arg(0)
	if profile == "" {
		profile = "coverage.out"
	}
	if err := run(*base, profile, *threshold); err != nil {
		fmt.Fprintln(os.Stderr, "covdiff:", err)
		os.Exit(1)
	}
}

func run(base, profile string, threshold float64) error {
	changed, err := ChangedLines(base)
	if err != nil {
		return err
	}
	blocks, err := ParseProfileFile(profile)
	if err != nil {
		return err
	}
	report := Evaluate(changed, blocks)
	if report.Total == 0 {
		fmt.Println("changed-coverage: N/A (no changed executable Go lines against", base+")")
		return nil
	}
	pct := 100 * float64(report.Covered) / float64(report.Total)
	fmt.Printf("changed-coverage: %d/%d executable changed lines (%.1f%%) vs %s\n", report.Covered, report.Total, pct, base)
	for _, f := range report.Files {
		fmt.Printf("  %s: %d/%d\n", f.File, f.Covered, f.Total)
		for _, line := range f.Uncovered {
			fmt.Printf("    uncovered changed line %d\n", line)
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
			file = m[1]
			if file == "/dev/null" || !Eligible(file) {
				file = ""
			}
			continue
		}
		if file == "" {
			continue
		}
		if m := changedHunk.FindStringSubmatch(text); m != nil {
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count == 0 { // pure deletion
				continue
			}
			if lines[file] == nil {
				lines[file] = map[int]bool{}
			}
			for i := start; i < start+count; i++ {
				lines[file][i] = true
			}
		}
	}
	return lines
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

// Evaluate determines, for every changed line, whether at least one
// covering profile block was executed. Lines outside any block (comments,
// blanks, import lines) are not executable and do not count.
func Evaluate(changed FileLines, blocks []Block) Report {
	byFile := map[string][]Block{}
	for _, b := range blocks {
		byFile[b.File] = append(byFile[b.File], b)
	}
	var report Report
	names := make([]string, 0, len(changed))
	for file := range changed {
		names = append(names, file)
	}
	sort.Strings(names)
	for _, file := range names {
		fileBlocks := byFile[file]
		if len(fileBlocks) == 0 {
			continue // file produced no statements (docs-only Go change, removed file)
		}
		lines := changed[file]
		numbers := make([]int, 0, len(lines))
		for n := range lines {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
		fr := FileReport{File: file}
		for _, n := range numbers {
			executable, covered := false, false
			for _, b := range fileBlocks {
				if n >= b.StartLine && n <= b.EndLine {
					executable = true
					if b.Count > 0 {
						covered = true
						break
					}
				}
			}
			if !executable {
				continue
			}
			fr.Total++
			report.Total++
			if covered {
				fr.Covered++
				report.Covered++
			} else {
				fr.Uncovered = append(fr.Uncovered, n)
			}
		}
		if fr.Total > 0 {
			report.Files = append(report.Files, fr)
		}
	}
	return report
}
