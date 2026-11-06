package config

import (
	"os"
	"sort"
	"strings"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// legacyEnvPrefix is the prefix the controller's settings had before the
// rename. hack/rename leaves this file alone, so this stays as written while
// EnvPrefix moves to the new prefix. While the two are equal, as they are until
// that rename, everything in this file does nothing: each name it would copy is
// already set under the name it would copy to.
//
// It stops being needed one release after the rename. The removal list is in
// docs/developer-guide/renaming.md.
const legacyEnvPrefix = "WVA_"

// RenamedScalingPolicyConfigMapName is the name the scaling-policy ConfigMap had
// before the project's rename. An admin's limiters in the policy namespace and a
// tenant's copy sit under this name until someone moves them, and the install
// does not write either, so it is read as a fall-back.
const RenamedScalingPolicyConfigMapName = "wva-scaling-policy-config"

// legacyAppLabelValue is the value of app.kubernetes.io/name before the rename.
const legacyAppLabelValue = "workload-variant-autoscaler"

// ConfigMapCacheSelector selects the ConfigMaps the multi-namespace cache holds:
// those labelled with the current value, and, for one release, the old one. A
// tenant's ConfigMap written before the rename carries the old label, and a
// ConfigMap outside the cache reads as NotFound however its name is resolved.
func ConfigMapCacheSelector(current string) labels.Selector {
	values := []string{current}
	if current != legacyAppLabelValue {
		values = append(values, legacyAppLabelValue)
	}
	req, err := labels.NewRequirement("app.kubernetes.io/name", selection.In, values)
	if err != nil {
		// Both values are constants of this repository.
		panic(err)
	}
	return labels.NewSelector().Add(*req)
}

// ApplyLegacyEnv makes the environment read as if every setting used the
// current name: each variable with the legacy prefix is copied to the current
// name, unless the current name is already set, which wins. It returns the
// names it copied and the names it discarded for that reason, for the caller to
// warn about once logging is up. Names only: a value may be a secret.
//
// Copying into the environment is deliberate. The settings are read in many
// places, through viper and os.Getenv alike, and this is the one spot that
// reaches all of them, so it runs first in main.
func ApplyLegacyEnv() (used, discarded []string) {
	return applyLegacyEnv(EnvPrefix, legacyEnvPrefix, os.Environ(), os.LookupEnv, os.Setenv)
}

func applyLegacyEnv(current, legacy string, environ []string,
	lookup func(string) (string, bool), set func(string, string) error) (used, discarded []string) {
	if current == legacy {
		return nil, nil
	}
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
			discarded = append(discarded, name)
			continue
		}
		if set(current+suffix, value) == nil {
			used = append(used, name)
		}
	}
	sort.Strings(used)
	sort.Strings(discarded)
	return used, discarded
}

// applyLegacyConfigKeys does for the config file what ApplyLegacyEnv does for
// the environment: a key with the legacy prefix is read as the current name,
// unless the file also has the current name. Set as a default, so it sits where
// the file does in viper's order, below the environment. viper lower-cases keys.
// It returns the keys it mapped and the keys it discarded.
func applyLegacyConfigKeys(v *viper.Viper, current string) (used, discarded []string) {
	if current == legacyEnvPrefix {
		return nil, nil
	}
	legacyLower, currentLower := strings.ToLower(legacyEnvPrefix), strings.ToLower(current)
	for _, key := range v.AllKeys() {
		suffix, ok := strings.CutPrefix(key, legacyLower)
		if !ok || !v.InConfig(key) {
			continue
		}
		if v.InConfig(currentLower + suffix) {
			discarded = append(discarded, key)
			continue
		}
		v.SetDefault(currentLower+suffix, v.Get(key))
		used = append(used, key)
	}
	sort.Strings(used)
	sort.Strings(discarded)
	return used, discarded
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
