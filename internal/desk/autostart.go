package desk

// Configured profile autostart (issue #17): operator YAML profiles marked
// autostart: true launch through the exact same validated pipeline as HTTP
// launches. Failures are reported per profile so one broken profile can
// never keep the manager (or its sibling profiles) from starting.

import (
	"log"
	"sort"

	"github.com/ekelhala/opentambo/internal/config"
	"github.com/ekelhala/opentambo/internal/desk/api"
)

// AutostartConfigured launches every configured profile with autostart
// enabled, in deterministic profile-name order. It is safe to call when no
// configuration file was loaded: it is then a no-op.
func (a *App) AutostartConfigured() {
	names := make([]string, 0, len(a.profilesCfg))
	for name := range a.profilesCfg {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := a.profilesCfg[name]
		if !profile.Autostart {
			continue
		}
		run, err := a.launchResolved(profileLaunch(profile))
		if err != nil {
			log.Printf("autostart: profile %s failed to launch: %v", name, err)
			continue
		}
		log.Printf("autostart: profile %s launched as run %s on port %d", name, run.ID, run.Launch.Port)
	}
}

// profileLaunch converts a resolved configuration profile into a launch
// spec. Context, GPU layers, args and env already carry their inherited
// values from config resolution; the cache policy arrives fully resolved so
// per-field defaults do not re-apply at launch time.
func profileLaunch(profile config.EffectiveProfile) api.Launch {
	spec := api.Launch{
		Name:      profile.Name,
		Model:     profile.Model,
		Port:      profile.Port,
		Context:   profile.Context,
		GPULayers: profile.GPULayers,
		Args:      append([]string{}, profile.Args...),
		Env:       copyStringMap(profile.Env),
	}
	if profile.Cache.Enabled {
		spec.PromptCache = &api.PromptCache{
			Enabled:        true,
			MaxBytes:       profile.Cache.MaxNamespace,
			MaxEntryBytes:  profile.Cache.MaxEntry,
			TTLSeconds:     profile.Cache.TTLSec,
			MinReuseTokens: int(profile.Cache.MinReuseTokens),
		}
	}
	return spec
}
