package desk

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/ekelhala/opentambo/internal/desk/api"
)

// Files on disk grouped the way users think about them: a model is the
// Hugging Face repository it came from (owner/name) and it holds one group
// per quantization. Shards of a split quantization belong to the same group
// and Primary is the file to hand to llama-server, which loads the
// remaining shards from the split metadata inside it.
// CatalogModel groups GGUF files by Hugging Face repository and quantization.
type CatalogModel = api.CatalogModel

type Quantization = api.Quantization

var (
	shardSuffix = regexp.MustCompile(`(?i)[-_.]\d{5}-of-\d{5}$`)
	quantSuffix = regexp.MustCompile(`(?i)[-_.]((?:UD-)?(?:IQ|AQ|Q)\d{1,2}(?:_[A-Z0-9]+)*|BF16|F16|F32|FP8)$`)
)

type modelMeta struct {
	repo       string // display name of the model, e.g. "owner/name"
	repoDir    string // repository directory relative to the model root
	familyKey  string // grouping key: directory + stem without shard or quant suffix
	family     string // display stem without shard or quant suffix
	quant      string // uppercased quant tag, empty when the filename has none
	projection bool
}

// Derive repository, quantization and family from a path shaped like the
// download layout "owner/name/revision/[subdir/]file.gguf". Loose files are
// named after their own stem so they still read as model + quantization.
func modelMetaOf(rel string) modelMeta {
	base := path.Base(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	stem = shardSuffix.ReplaceAllString(stem, "")
	quant := ""
	if m := quantSuffix.FindStringSubmatch(stem); m != nil {
		quant = strings.ToUpper(m[0][1:])
		stem = strings.TrimRight(stem[:len(stem)-len(m[0])], "-_. ")
	}
	meta := modelMeta{familyKey: path.Dir(rel) + "/" + stem, family: stem, quant: quant,
		projection: strings.Contains(strings.ToLower(rel), "mmproj")}
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) >= 3:
		meta.repo, meta.repoDir = parts[0]+"/"+parts[1], parts[0]+"/"+parts[1]
	case len(parts) == 2:
		meta.repo, meta.repoDir = parts[0], parts[0]
	default:
		meta.repo, meta.repoDir = stem, "."
	}
	if meta.repo == "" {
		meta.repo = base
	}
	return meta
}

func catalogFrom(models []Model) []CatalogModel {
	type entry struct {
		repo   *CatalogModel
		groups []*Quantization
		byKey  map[string]*Quantization
	}
	byRepo := map[string]*entry{}
	for _, model := range models {
		meta := modelMetaOf(model.Path)
		e := byRepo[meta.repo]
		if e == nil {
			e = &entry{repo: &CatalogModel{Repo: meta.repo, Path: meta.repoDir}, byKey: map[string]*Quantization{}}
			byRepo[meta.repo] = e
		}
		key := meta.familyKey + "\x00" + meta.quant
		group := e.byKey[key]
		if group == nil {
			group = &Quantization{Label: meta.quant, Family: meta.family, Projection: meta.projection}
			e.byKey[key] = group
			e.groups = append(e.groups, group)
		}
		group.Files = append(group.Files, model)
		group.Size += model.Size
		group.Shards++
		if model.Modified.After(group.Modified) {
			group.Modified = model.Modified
		}
		e.repo.Size += model.Size
		if model.Modified.After(e.repo.Modified) {
			e.repo.Modified = model.Modified
		}
	}
	repos := make([]CatalogModel, 0, len(byRepo))
	for _, e := range byRepo {
		sort.Slice(e.groups, func(a, b int) bool {
			q, p := e.groups[a], e.groups[b]
			if q.Label != p.Label {
				return q.Label < p.Label
			}
			return q.Family < p.Family
		})
		repo := *e.repo
		repo.Quantizations = make([]Quantization, 0, len(e.groups))
		for _, group := range e.groups {
			sort.Slice(group.Files, func(a, b int) bool { return group.Files[a].Path < group.Files[b].Path })
			group.Primary = group.Files[0].Path
			repo.Quantizations = append(repo.Quantizations, *group)
		}
		repos = append(repos, repo)
	}
	sort.Slice(repos, func(a, b int) bool { return repos[a].Repo < repos[b].Repo })
	return repos
}

// Alias for a launched server: the filename without extension and without a
// shard counter, so split models keep a readable name.
func defaultModelName(rel string) string {
	stem := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	return shardSuffix.ReplaceAllString(stem, "")
}
