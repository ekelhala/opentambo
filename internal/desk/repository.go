package desk

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/ekelhala/opentambo/internal/desk/api"
	"github.com/gomlx/go-huggingface/hub"
)

// RepositoryFile and RepositoryQuantization describe what a Hugging Face
// repository holds, grouped by quantization so users pick a level instead of
// typing shard filenames. Files land in the shelf under the same rules as any
// manual download. These are the generated transport records from
// api/openapi.yaml.
type RepositoryFile = api.RepositoryFile
type RepositoryQuantization = api.RepositoryQuantization
type repositoryListing = api.RepositoryListing

// Group repository-relative GGUF names by quantization using the same naming
// rules as the local catalog so both views agree. Non-GGUF names are ignored
// and files within a group are sorted, so shards queue in order.
func groupRepositoryFiles(files []RepositoryFile) []RepositoryQuantization {
	groups := map[string]*RepositoryQuantization{}
	order := []string{}
	for _, file := range files {
		if !strings.HasSuffix(strings.ToLower(file.Name), ".gguf") {
			continue
		}
		meta := modelMetaOf(file.Name)
		key := meta.familyKey + "\x00" + meta.quant
		group := groups[key]
		if group == nil {
			group = &RepositoryQuantization{Label: meta.quant, Family: meta.family, Projection: meta.projection}
			groups[key] = group
			order = append(order, key)
		}
		group.Files = append(group.Files, file)
		group.Size += file.Size
		group.Shards++
	}
	quants := make([]RepositoryQuantization, 0, len(order))
	for _, key := range order {
		group := *groups[key]
		sort.Slice(group.Files, func(a, b int) bool { return group.Files[a].Name < group.Files[b].Name })
		quants = append(quants, group)
	}
	sort.Slice(quants, func(a, b int) bool {
		if quants[a].Label != quants[b].Label {
			return quants[a].Label < quants[b].Label
		}
		return quants[a].Family < quants[b].Family
	})
	return quants
}

// GET /api/repository?repo=owner/name&revision=main lists the GGUF files a
// public Hugging Face repository offers, bundled per quantization. Listing
// reads only the repo tree, not the files themselves.
func (a *App) browseRepository(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	revision := strings.TrimSpace(r.URL.Query().Get("revision"))
	if err := validateRepository(repo, revision); err != nil {
		fail(w, err)
		return
	}
	if revision == "" {
		revision = "main"
	}
	remote := hub.New(repo).WithRevision(revision).WithProgressBar(false)
	remote.Verbosity = 0
	ggufFiles := []RepositoryFile{}
	seen := 0
	for info, err := range remote.IterFileInfos() {
		if err != nil {
			fail(w, errors.New("could not list the repository: "+err.Error()))
			return
		}
		seen++
		if seen > 10000 {
			fail(w, errors.New("repository listing is too large"))
			return
		}
		if info == nil || !strings.HasSuffix(strings.ToLower(info.Name), ".gguf") {
			continue
		}
		ggufFiles = append(ggufFiles, RepositoryFile{Name: info.Name, Size: info.Size})
	}
	respond(w, 200, repositoryListing{Repo: repo, Revision: revision, Quantizations: groupRepositoryFiles(ggufFiles)})
}
