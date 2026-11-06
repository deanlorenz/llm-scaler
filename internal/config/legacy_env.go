package config

import (
	"os"
	"sort"
	"strings"

	"github.com/spf13/viper"
)

// legacyEnvPrefix is the prefix the controller's settings had before the
// rename. hack/rename leaves this file alone, so this stays as written while
// EnvPrefix moves to the new prefix. While the two are equal, as they are until
// that rename, everything in this file does nothing: each name it would copy is
// already set under the name it would copy to.
//
// It stops being needed one release after the rename: delete this file, the
// ApplyLegacyEnv call in main, and the two helpers' call sites.
const legacyEnvPrefix = "WVA_"

// ApplyLegacyEnv makes the environment read as if every setting used the
// current name: each variable with the legacy prefix is copied to the current
// name, unless the current name is already set, which wins. It returns the
// names it copied, for the caller to warn about once logging is up.
//
// Copying into the environment is deliberate. The settings are read in many
// places, through viper and os.Getenv alike, and this is the one spot that
// reaches all of them, so it runs first in main.
func ApplyLegacyEnv() []string {
	return applyLegacyEnv(EnvPrefix, legacyEnvPrefix, os.Environ(), os.LookupEnv, os.Setenv)
}

func applyLegacyEnv(current, legacy string, environ []string,
	lookup func(string) (string, bool), set func(string, string) error) []string {
	var used []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		suffix, ok := strings.CutPrefix(name, legacy)
		if !ok {
			continue
		}
		if _, taken := lookup(current + suffix); taken {
			continue
		}
		if set(current+suffix, value) == nil {
			used = append(used, name)
		}
	}
	sort.Strings(used)
	return used
}

// applyLegacyConfigKeys does for the config file what ApplyLegacyEnv does for
// the environment: a key with the legacy prefix is read as the current name,
// unless the file also has the current name. Set as a default, so it sits where
// the file does in viper's order, below the environment. viper lower-cases keys.
// It returns the keys it mapped.
func applyLegacyConfigKeys(v *viper.Viper, current string) []string {
	if current == legacyEnvPrefix {
		return nil
	}
	legacyLower, currentLower := strings.ToLower(legacyEnvPrefix), strings.ToLower(current)
	var used []string
	for _, key := range v.AllKeys() {
		suffix, ok := strings.CutPrefix(key, legacyLower)
		if !ok || !v.InConfig(key) || v.InConfig(currentLower+suffix) {
			continue
		}
		v.SetDefault(currentLower+suffix, v.Get(key))
		used = append(used, key)
	}
	sort.Strings(used)
	return used
}

// lookupWithLegacy reads key from a decoded settings map, falling back to the
// same setting under the legacy prefix.
func lookupWithLegacy(m map[string]any, key string) (any, bool) {
	return lookupPrefixed(m, key, EnvPrefix, legacyEnvPrefix)
}

func lookupPrefixed(m map[string]any, key, current, legacy string) (any, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	if suffix, ok := strings.CutPrefix(key, current); ok {
		v, ok := m[legacy+suffix]
		return v, ok
	}
	return nil, false
}
