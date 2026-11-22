//go:build integration

// Package integration runs the real OpenTambo binary against a real,
// pinned llama-server build on CPU (issue #19). Nothing here is mocked or
// skipped: a missing fixture is a hard failure, and the suite executes the
// management API, the /v1 gateway and the prompt-cache lifecycle exactly as
// an operator would. The manager binary is compiled with -cover; its
// per-process coverage lands in the shared GOCOVERDIR and TestMain merges
// it into coverage.integration.out.
package integration

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	llamaRelease = "b12531"
	llamaAsset   = "llama-b12531-bin-" + "ubuntu-x64.tar.gz"
	llamaSHA256  = "5c1bf73c8c338d7934bbe7b43827496685f69b2e2afce2de9c536f755dd94a20"
	llamaURL     = "https://github.com/ggml-org/llama.cpp/releases/download/" + llamaRelease + "/" + llamaAsset

	// modelRelPath is the GGUF taken from this repository's model shelf
	// when present; full-attention llama architecture, small enough for CPU
	// CI. Fresh checkouts carry no weights, so the same pinned quantization
	// is fetched from Hugging Face and verified against modelSHA256.
	modelRelPath = "unsloth/SmolLM2-135M-Instruct-GGUF/main/SmolLM2-135M-Instruct-Q4_K_M.gguf"
	modelURL     = "https://huggingface.co/unsloth/SmolLM2-135M-Instruct-GGUF/resolve/main/SmolLM2-135M-Instruct-Q4_K_M.gguf"
	modelSHA256  = "ed5fa30c487b282ec156c29062f1222e5c20875a944ac98289dbd242e947f747"

	// hybridModelRelPath pins a hybrid-architecture fixture (LFM2 mixes
	// causal-convolution recurrent layers with attention). It is the
	// smallest GGUF llama.cpp supports that exercises the recurrent-memory
	// save/restore path, so the hybrid prompt-cache scenarios run on CPU CI
	// exactly like the full-attention ones.
	hybridModelRelPath = "LiquidAI/LFM2-1.2B-GGUF/main/LFM2-1.2B-Q4_K_M.gguf"
	hybridModelURL     = "https://huggingface.co/LiquidAI/LFM2-1.2B-GGUF/resolve/main/LFM2-1.2B-Q4_K_M.gguf"
	hybridModelSHA256  = "55175400e3f509a9616227afeffd58d87e80b9f628a5d3d54ada884d85221fed"
)

// shelfFixture names one pinned GGUF that prepareModelShelf provisions.
type shelfFixture struct {
	relPath string
	url     string
	sha256  string
}

var shelfFixtures = []shelfFixture{
	{modelRelPath, modelURL, modelSHA256},
	{hybridModelRelPath, hybridModelURL, hybridModelSHA256},
}

var (
	repoRoot     string
	sharedModels string // copied GGUF shelf shared by all tests
	sharedBinary string // instrumented opentambo binary, built once
	coverDir     string // GOCOVERDIR root for manager subprocesses
)

func TestMain(m *testing.M) {
	code, err := runMain(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration harness:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runMain(m *testing.M) (int, error) {
	var err error
	if repoRoot, err = findRepoRoot(); err != nil {
		return 1, err
	}
	if sharedModels, err = prepareModelShelf(); err != nil {
		return 1, err
	}
	coverDir, err = os.MkdirTemp("", "opentambo-integration-cover-*")
	if err != nil {
		return 1, err
	}
	if sharedBinary, err = buildInstrumentedBinary(); err != nil {
		return 1, err
	}
	code := m.Run()
	if err := mergeCoverage(); err != nil {
		fmt.Fprintln(os.Stderr, "integration coverage merge failed:", err)
		code = 1
	}
	return code, nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// buildInstrumentedBinary compiles the binary under test with -cover so the
// subprocesses launched by the suite emit runtime coverage.
func buildInstrumentedBinary() (string, error) {
	outDir, err := os.MkdirTemp("", "opentambo-integration-bin-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(outDir, "opentambo")
	cmd := exec.Command("go", "build", "-cover", "-o", binary, "./cmd/opentambo")
	cmd.Dir = repoRoot
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build -cover: %w\n%s", err, combined)
	}
	return binary, nil
}

// mergeCoverage turns the instrumented subprocess profiles into
// coverage.integration.out at the repository root. Zero profiles means the
// suite never executed the real binary, which fails the gate (issue #19).
func mergeCoverage() error {
	entries, err := os.ReadDir(coverDir)
	if err != nil {
		return err
	}
	profiles := 0
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(coverDir, entry.Name()))
		if err != nil {
			return err
		}
		local := 0
		for _, e := range sub {
			if strings.HasPrefix(e.Name(), "covcounters.") {
				local++
			}
		}
		if local > 0 {
			profiles += local
			dirs = append(dirs, filepath.Join(coverDir, entry.Name()))
		}
	}
	if profiles == 0 {
		return fmt.Errorf("no instrumented subprocess profiles in %s: the suite ran zero real-binary tests", coverDir)
	}
	out := filepath.Join(repoRoot, "coverage.integration.out")
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+strings.Join(dirs, ","), "-o="+out)
	cmd.Dir = repoRoot
	if combined, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go tool covdata textfmt: %w\n%s", err, combined)
	}
	fmt.Printf("integration coverage: merged %d subprocess profiles into %s\n", profiles, out)
	return nil
}

// fixtureCacheDir returns the shared on-disk cache for downloaded fixtures,
// overridable with OPENTAMBO_FIXTURE_CACHE.
func fixtureCacheDir() string {
	if cache := os.Getenv("OPENTAMBO_FIXTURE_CACHE"); cache != "" {
		return cache
	}
	return filepath.Join(os.TempDir(), "opentambo-fixtures")
}

// fixturePath returns the pinned GGUF for one shelf fixture: the
// repository's model shelf when it carries the file, otherwise a fixture-cache
// copy fetched from Hugging Face and verified against the pinned SHA-256. Fresh
// CI checkouts hold no model weights, so the harness provisions its own
// identical file; failures are fatal, never skipped.
func (f shelfFixture) fixturePath() (string, error) {
	source := filepath.Join(repoRoot, "models", filepath.FromSlash(f.relPath))
	if _, err := os.Stat(source); err == nil {
		return source, nil
	}
	target := filepath.Join(fixtureCacheDir(), "models", filepath.FromSlash(f.relPath))
	if !fileHasSHA(target, f.sha256) {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		fmt.Printf("integration harness: fetching pinned model fixture %s\n", f.url)
		if err := downloadFile(f.url, target); err != nil {
			return "", fmt.Errorf("model fixture %s is required (no skipping): %w", f.relPath, err)
		}
		if !fileHasSHA(target, f.sha256) {
			return "", fmt.Errorf("downloaded model fixture does not match the pinned SHA-256 %s", f.sha256)
		}
	}
	return target, nil
}

func prepareModelShelf() (string, error) {
	shelf, err := os.MkdirTemp("", "opentambo-integration-models-*")
	if err != nil {
		return "", err
	}
	for _, fixture := range shelfFixtures {
		source, err := fixture.fixturePath()
		if err != nil {
			return "", err
		}
		target := filepath.Join(shelf, filepath.FromSlash(fixture.relPath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		input, err := os.Open(source)
		if err != nil {
			return "", err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			input.Close()
			return "", err
		}
		_, copyErr := io.Copy(output, input)
		closeIn := input.Close()
		closeOut := output.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeIn != nil {
			return "", closeIn
		}
		if closeOut != nil {
			return "", closeOut
		}
	}
	return shelf, nil
}

// ensureLlamaServer returns an executable pinned llama-server, downloading
// and SHA-256-verifying the release archive on first use. A corrupt cache is
// replaced, never trusted; failures are fatal, never skipped.
func ensureLlamaServer(t *testing.T) string {
	t.Helper()
	cache := fixtureCacheDir()
	dir := filepath.Join(cache, "llama-"+llamaRelease)
	marker := filepath.Join(dir, ".pinned-sha256")
	if binary := findFixtureBinary(dir); binary != "" && executable(binary) {
		if contents, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(contents)) == llamaSHA256 {
			return binary
		}
	}
	tarball := filepath.Join(cache, llamaAsset)
	if !fileMatchesSHA(tarball) {
		if err := os.MkdirAll(cache, 0o755); err != nil {
			t.Fatal(err)
		}
		download(t, llamaURL, tarball)
		if !fileMatchesSHA(tarball) {
			t.Fatalf("downloaded %s does not match the pinned SHA-256 %s", llamaURL, llamaSHA256)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := extractTarball(tarball, dir); err != nil {
		t.Fatalf("extracting pinned fixture: %v", err)
	}
	binary := findFixtureBinary(dir)
	if binary == "" || !executable(binary) {
		t.Fatalf("archive %s did not contain an executable llama-server", llamaAsset)
	}
	if err := os.WriteFile(marker, []byte(llamaSHA256+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("fixture llama-server does not run: %v\n%s", err, out)
	}
	t.Logf("pinned llama-server %s (%s)", llamaRelease, strings.TrimSpace(string(out)))
	return binary
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

// findFixtureBinary locates the llama-server executable in an extracted
// archive regardless of the layout the release chose.
func findFixtureBinary(dir string) string {
	var found string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found != "" || entry.IsDir() {
			return nil
		}
		if entry.Name() == "llama-server" {
			found = path
		}
		return nil
	})
	return found
}

func fileMatchesSHA(path string) bool {
	return fileHasSHA(path, llamaSHA256)
}

func fileHasSHA(path, want string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == want
}

func downloadFile(url, dest string) error {
	response, err := http.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, response.Body)
	return err
}

func download(t *testing.T, url, dest string) {
	t.Helper()
	t.Logf("downloading %s", url)
	if err := downloadFile(url, dest); err != nil {
		t.Fatalf("downloading fixture: %v", err)
	}
}

func extractTarball(tarball, dir string) error {
	file, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || strings.HasPrefix(name, "..") || strings.Contains(name, string(filepath.Separator)+"..") {
			continue // refuse traversal, whatever the archive claims
		}
		target := filepath.Join(dir, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode) & 0o755
			output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			written, err := io.Copy(output, reader)
			closeErr := output.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if written != header.Size {
				return fmt.Errorf("short extract of %s", name)
			}
		case tar.TypeSymlink:
			// The llama.cpp archive ships its shared-library soname links as
			// symlinks; the server binary cannot start without them.
			link := filepath.Clean(filepath.Join(filepath.Dir(target), header.Linkname))
			if !strings.HasPrefix(link, dir) {
				return fmt.Errorf("symlink %s escapes the fixture directory", name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// manager is a running OpenTambo subprocess with its own data directory and
// coverage bucket.
type manager struct {
	t        *testing.T
	base     string
	dataDir  string
	cmd      *exec.Cmd
	log      *os.File
	coverDir string
}

// startManager launches the instrumented binary on a free loopback port and
// waits for /healthz, failing (not skipping) if it never becomes healthy.
func startManager(t *testing.T, llamaBinary string) *manager {
	t.Helper()
	return startManagerWith(t, llamaBinary, "")
}

// startManagerWith restarts a manager over an existing data directory when
// dataDir is set, which is how the suite proves state and prompt-cache
// survival across an OpenTambo restart.
func startManagerWith(t *testing.T, llamaBinary, dataDir string) *manager {
	t.Helper()
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if dataDir == "" {
		if dataDir, err = os.MkdirTemp("", "opentambo-integration-data-*"); err != nil {
			t.Fatal(err)
		}
	}
	cover, err := os.MkdirTemp(coverDir, "run-*")
	if err != nil {
		t.Fatal(err)
	}
	logFile, err := os.CreateTemp(t.TempDir(), "opentambo.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sharedBinary,
		"-listen", fmt.Sprintf("127.0.0.1:%d", port),
		"-models", sharedModels,
		"-llama-server", llamaBinary,
		"-data", dataDir)
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+cover)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	m := &manager{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), dataDir: dataDir, cmd: cmd, log: logFile, coverDir: cover}
	t.Cleanup(m.stop)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		response, err := client.Get(m.base + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return m
			}
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			m.dumpLog()
			t.Fatalf("manager exited during startup: %s", cmd.ProcessState)
		}
		if time.Now().After(deadline) {
			m.dumpLog()
			t.Fatalf("manager never became healthy at %s", m.base)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (m *manager) dumpLog() {
	if contents, err := os.ReadFile(m.log.Name()); err == nil {
		fmt.Fprintf(os.Stderr, "---- opentambo log (%s) ----\n%s\n", m.dataDir, contents)
	}
}

// stop terminates the manager with SIGTERM and requires a clean, timely
// exit; the graceful path must reap all managed children.
func (m *manager) stop() {
	if m.cmd.Process == nil {
		return
	}
	if m.cmd.ProcessState != nil && m.cmd.ProcessState.Exited() {
		return
	}
	if err := m.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		m.t.Errorf("signaling manager: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- m.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			m.dumpLog()
			m.t.Errorf("graceful shutdown failed: %v", err)
		}
	case <-time.After(20 * time.Second):
		_ = m.cmd.Process.Kill()
		m.dumpLog()
		m.t.Errorf("manager did not exit within 20s of SIGTERM")
	}
	// The listener must be released before a sibling test reuses the port.
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(m.base, "http://"), time.Second)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			m.t.Errorf("manager port still open after exit")
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// request performs an HTTP call against the manager and decodes a JSON
// object response. Status mismatches are test failures.
func (m *manager) request(method, path, body string, wantStatus int) map[string]any {
	m.t.Helper()
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, m.base+path, payload)
	if err != nil {
		m.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 5 * time.Minute}).Do(request)
	if err != nil {
		m.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		m.t.Fatalf("%s %s: reading body: %v", method, path, err)
	}
	if wantStatus >= 0 && response.StatusCode != wantStatus {
		m.dumpLog()
		m.t.Fatalf("%s %s: status %d, want %d (body: %s)", method, path, response.StatusCode, wantStatus, contents)
	}
	var decoded map[string]any
	if len(contents) > 0 {
		if err := json.Unmarshal(contents, &decoded); err != nil {
			m.t.Fatalf("%s %s: invalid JSON %q: %v", method, path, contents, err)
		}
	}
	return decoded
}

func (m *manager) get(path string, wantStatus int) map[string]any {
	m.t.Helper()
	return m.request(http.MethodGet, path, "", wantStatus)
}

func (m *manager) post(path, body string, wantStatus int) map[string]any {
	m.t.Helper()
	return m.request(http.MethodPost, path, body, wantStatus)
}

func (m *manager) delete(path string, wantStatus int) map[string]any {
	m.t.Helper()
	return m.request(http.MethodDelete, path, "", wantStatus)
}

// launchSpec builds a CPU launch body for the shared shelf model.
func launchSpec(name string, port int, extra map[string]any) string {
	spec := map[string]any{
		"name":       name,
		"model":      modelRelPath,
		"port":       port,
		"context":    2048,
		"gpu_layers": 0,
	}
	for key, value := range extra {
		spec[key] = value
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// launchRun starts a run through the management API and waits until the
// child itself reports healthy.
func (m *manager) launchRun(name string, port int, extra map[string]any) map[string]any {
	m.t.Helper()
	run := m.post("/api/runs", launchSpec(name, port, extra), http.StatusCreated)
	if run["id"] == nil {
		m.t.Fatalf("launch response has no id: %v", run)
	}
	id, _ := run["id"].(string)
	deadline := time.Now().Add(90 * time.Second)
	for {
		health := m.get("/api/runs/"+id+"/health", http.StatusOK)
		if child, ok := health["response"].(map[string]any); ok && child["status"] == "ok" {
			return run
		}
		if status, ok := health["status"].(string); ok && (status == "stopped" || status == "failed") {
			m.dumpLog()
			m.t.Fatalf("run %s settled unhealthy: %v", id, health)
		}
		if time.Now().After(deadline) {
			m.dumpLog()
			m.t.Fatalf("run %s never became healthy: last health %v", id, health)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (m *manager) stopRun(id string) {
	m.t.Helper()
	m.post("/api/runs/"+id+"/stop", "", http.StatusAccepted)
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, item := range runList(m.get("/api/state", http.StatusOK)) {
			if item["id"] == id {
				if status, _ := item["status"].(string); status == "stopped" || status == "failed" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			m.t.Fatalf("run %s never stopped", id)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// runList extracts the runs array from a /api/state response.
func runList(state map[string]any) []map[string]any {
	items, _ := state["runs"].([]any)
	runs := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if run, ok := item.(map[string]any); ok {
			runs = append(runs, run)
		}
	}
	return runs
}

// completion posts a non-streaming chat completion through the gateway.
func (m *manager) completion(model, prompt string, extra map[string]any) map[string]any {
	m.t.Helper()
	body := chatBody(model, prompt, false, extra)
	response := m.post("/v1/chat/completions", body, http.StatusOK)
	choices, _ := response["choices"].([]any)
	if len(choices) == 0 {
		m.t.Fatalf("completion without choices: %v", response)
	}
	return response
}

func chatBody(model, prompt string, stream bool, extra map[string]any) string {
	body := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": prompt},
		},
		"max_tokens": 24,
		"stream":     stream,
	}
	for key, value := range extra {
		body[key] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// numeric extracts a JSON number as float64 for assertions.
func numeric(t *testing.T, value any, context string) float64 {
	t.Helper()
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("%s is %T (%v), want a JSON number", context, value, value)
	}
	return number
}
