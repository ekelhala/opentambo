package desk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModelMetaOf(t *testing.T) {
	cases := []struct {
		rel, repo, family, quant string
		projection               bool
	}{
		{"owner/repo/main/model.gguf", "owner/repo", "model", "", false},
		{"owner/repo/main/LLaMA-3.2-1B-Q4_K_M.gguf", "owner/repo", "LLaMA-3.2-1B", "Q4_K_M", false},
		{"owner/repo/main/model-q2_k.gguf", "owner/repo", "model", "Q2_K", false},
		{"owner/repo/main/Qwen3-0.6B-UD-Q4_K_XL.gguf", "owner/repo", "Qwen3-0.6B", "UD-Q4_K_XL", false},
		{"owner/repo/main/model-IQ4_XS.gguf", "owner/repo", "model", "IQ4_XS", false},
		{"owner/repo/main/model-F16.gguf", "owner/repo", "model", "F16", false},
		{"owner/repo/main/mmproj-model-f16.gguf", "owner/repo", "mmproj-model", "F16", true},
		{"owner/repo/main/model-00001-of-00002.gguf", "owner/repo", "model", "", false},
		{"owner/repo/main/Llama-Q8_0-00002-of-00002.gguf", "owner/repo", "Llama", "Q8_0", false},
		{"loose-model-q4_k_m.gguf", "loose-model", "loose-model", "Q4_K_M", false},
		{"dir/model.gguf", "dir", "model", "", false},
	}
	for _, c := range cases {
		meta := modelMetaOf(c.rel)
		if meta.repo != c.repo || meta.family != c.family || meta.quant != c.quant || meta.projection != c.projection {
			t.Errorf("modelMetaOf(%q) = %+v, want repo=%q family=%q quant=%q projection=%v", c.rel, meta, c.repo, c.family, c.quant, c.projection)
		}
	}
}

func TestCatalogGroupsRepositoriesQuantizationsAndShards(t *testing.T) {
	now := time.Now()
	models := []Model{
		{Path: "bartowski/repo-a/main/Q8_0/repo-a-Q8_0-00002-of-00002.gguf", Size: 300, Modified: now},
		{Path: "bartowski/repo-a/main/Q8_0/repo-a-Q8_0-00001-of-00002.gguf", Size: 400, Modified: now.Add(time.Second)},
		{Path: "bartowski/repo-a/main/repo-a-Q4_K_M.gguf", Size: 200, Modified: now},
		{Path: "ggml-org/repo-b/main/model.gguf", Size: 50, Modified: now},
		{Path: "loose-q2_k.gguf", Size: 10, Modified: now},
	}
	catalog := catalogFrom(models)
	if len(catalog) != 3 {
		t.Fatalf("expected 3 repositories, got %+v", catalog)
	}
	if catalog[0].Repo != "bartowski/repo-a" || catalog[1].Repo != "ggml-org/repo-b" || catalog[2].Repo != "loose" {
		t.Fatalf("bad repository order: %s %s %s", catalog[0].Repo, catalog[1].Repo, catalog[2].Repo)
	}
	a := catalog[0]
	if a.Path != "bartowski/repo-a" || a.Size != 900 {
		t.Fatalf("bad totals: %+v", a)
	}
	if len(a.Quantizations) != 2 {
		t.Fatalf("expected 2 quantizations: %+v", a.Quantizations)
	}
	q4, q8 := a.Quantizations[0], a.Quantizations[1]
	if q4.Label != "Q4_K_M" || q4.Shards != 1 || q4.Primary != "bartowski/repo-a/main/repo-a-Q4_K_M.gguf" {
		t.Fatalf("bad Q4 group: %+v", q4)
	}
	if q8.Label != "Q8_0" || q8.Shards != 2 || q8.Size != 700 {
		t.Fatalf("bad Q8 group: %+v", q8)
	}
	if q8.Primary != "bartowski/repo-a/main/Q8_0/repo-a-Q8_0-00001-of-00002.gguf" {
		t.Fatalf("shards must launch from the first shard: %s", q8.Primary)
	}
	if loose := catalog[2]; loose.Repo != "loose" || len(loose.Quantizations) != 1 || loose.Quantizations[0].Label != "Q2_K" {
		t.Fatalf("loose files must read as model + quantization: %+v", loose)
	}
}

func TestGroupRepositoryFiles(t *testing.T) {
	groups := groupRepositoryFiles([]RepositoryFile{
		{Name: "Big-Model-Q4_K_M.gguf", Size: 100},
		{Name: "Big-Model-IQ4_XS-00001-of-00003.gguf", Size: 60},
		{Name: "Big-Model-IQ4_XS-00003-of-00003.gguf", Size: 60},
		{Name: "Big-Model-IQ4_XS-00002-of-00003.gguf", Size: 60},
		{Name: "README.md", Size: 5},
	})
	if len(groups) != 2 {
		t.Fatalf("groups: %+v", groups)
	}
	if groups[0].Label != "IQ4_XS" || groups[0].Shards != 3 || groups[0].Size != 180 {
		t.Fatalf("bad shard group: %+v", groups[0])
	}
	if groups[1].Label != "Q4_K_M" {
		t.Fatalf("bad order: %+v", groups)
	}
}

func TestDefaultModelNameStripsShardCounter(t *testing.T) {
	if got := defaultModelName("owner/repo/main/model-00001-of-00002.gguf"); got != "model" {
		t.Fatalf("got %q", got)
	}
	if got := defaultModelName("owner/repo/main/model-q4_k_m.gguf"); got != "model-q4_k_m" {
		t.Fatalf("got %q", got)
	}
}

func TestStateIncludesCatalogAndRepositoryValidation(t *testing.T) {
	a := testApp(t, "unused")
	if err := os.MkdirAll(filepath.Join(a.models, "owner", "repo", "main"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.models, "owner", "repo", "main", "model-q4_k_m.gguf"), []byte("GGUFtest"), 0640); err != nil {
		t.Fatal(err)
	}
	rec := request(t, a, "GET", "/api/state", "")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var state struct {
		Catalog []CatalogModel `json:"catalog"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Catalog) != 1 || state.Catalog[0].Repo != "owner/repo" || state.Catalog[0].Quantizations[0].Label != "Q4_K_M" {
		t.Fatalf("bad catalog in state: %s", rec.Body.String())
	}
	for _, query := range []string{"repo=evil", "repo=owner%2Frepo&revision=../main", "repo="} {
		rec := request(t, a, "GET", "/api/repository?"+query, "")
		if rec.Code != 400 {
			t.Fatalf("accepted %q: %d", query, rec.Code)
		}
	}
}
