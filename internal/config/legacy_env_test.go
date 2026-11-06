package config

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/labels"
)

func fakeEnv(env map[string]string) ([]string, func(string) (string, bool), func(string, string) error) {
	environ := make([]string, 0, len(env))
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	set := func(k, v string) error { env[k] = v; return nil }
	return environ, lookup, set
}

func TestApplyLegacyEnv_CopiesAnOldNameAndNewWins(t *testing.T) {
	env := map[string]string{
		"WVA_PASSIVE":         "true",
		"WVA_LIMITED_MODE":    "true",
		"LLMSM_LIMITED_MODE":  "false",
		"WVA_EMPTY":           "",
		"SOMETHING_ELSE":      "x",
		"NOT_WVA_SCALE_THING": "x",
	}
	environ, lookup, set := fakeEnv(env)

	used, discarded := applyLegacyEnv("LLMSM_", "WVA_", environ, lookup, set)

	if want := []string{"WVA_EMPTY", "WVA_PASSIVE"}; !reflect.DeepEqual(used, want) {
		t.Errorf("used = %v, want %v", used, want)
	}
	if want := []string{"WVA_LIMITED_MODE"}; !reflect.DeepEqual(discarded, want) {
		t.Errorf("discarded = %v, want %v", discarded, want)
	}
	if env["LLMSM_PASSIVE"] != "true" {
		t.Errorf("LLMSM_PASSIVE = %q, the old value did not arrive", env["LLMSM_PASSIVE"])
	}
	if env["LLMSM_LIMITED_MODE"] != "false" {
		t.Errorf("LLMSM_LIMITED_MODE = %q, the old name beat the new one", env["LLMSM_LIMITED_MODE"])
	}
	if v, ok := env["LLMSM_EMPTY"]; !ok || v != "" {
		t.Errorf("an old name set empty must arrive set and empty, got %q %v", v, ok)
	}
	if _, ok := env["LLMSM_SCALE_THING"]; ok {
		t.Error("a name that merely contains the prefix was copied")
	}
}

func TestApplyLegacyEnv_DoesNothingBeforeTheRename(t *testing.T) {
	env := map[string]string{"WVA_PASSIVE": "true"}
	environ, lookup, set := fakeEnv(env)
	if used, discarded := applyLegacyEnv("WVA_", "WVA_", environ, lookup, set); used != nil || discarded != nil {
		t.Errorf("used = %v, discarded = %v, want nothing while the prefixes are equal", used, discarded)
	}
	if len(env) != 1 {
		t.Errorf("environment changed: %v", env)
	}
}

func TestApplyLegacyEnv_TheTreeIsConsistent(t *testing.T) {
	// EnvPrefix is the old prefix until the rename and the new one after it.
	// Anything else means the tool missed it or broke it.
	if EnvPrefix != "WVA_" && EnvPrefix != "LLMSM_" {
		t.Errorf("EnvPrefix = %q", EnvPrefix)
	}
	if legacyEnvPrefix != "WVA_" {
		t.Errorf("legacyEnvPrefix = %q: the rename reached a file it must leave alone", legacyEnvPrefix)
	}
}

func readConfig(t *testing.T, yaml string) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(yaml)); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestApplyLegacyConfigKeys(t *testing.T) {
	v := readConfig(t, "WVA_PASSIVE: true\nWVA_LIMITED_MODE: true\nLLMSM_LIMITED_MODE: false\nOTHER: x\n")
	v.SetDefault("LLMSM_PASSIVE", false)
	v.SetDefault("WVA_DEFAULT_ONLY", true)

	used, discarded := applyLegacyConfigKeys(v, "LLMSM_")

	if want := []string{"wva_passive"}; !reflect.DeepEqual(used, want) {
		t.Errorf("used = %v, want %v", used, want)
	}
	if want := []string{"wva_limited_mode"}; !reflect.DeepEqual(discarded, want) {
		t.Errorf("discarded = %v, want %v", discarded, want)
	}
	if !v.GetBool("LLMSM_PASSIVE") {
		t.Error("the old key in the file did not beat the new key's default")
	}
	if v.GetBool("LLMSM_LIMITED_MODE") {
		t.Error("the old key beat the new key in the same file")
	}
}

func TestApplyLegacyConfigKeys_EnvironmentStillWins(t *testing.T) {
	t.Setenv("LLMSM_PASSIVE", "false")
	v := readConfig(t, "WVA_PASSIVE: true\n")
	v.AutomaticEnv()
	applyLegacyConfigKeys(v, "LLMSM_")
	if v.GetBool("LLMSM_PASSIVE") {
		t.Error("a value from the file beat the environment")
	}
}

func TestApplyLegacyConfigKeys_DoesNothingBeforeTheRename(t *testing.T) {
	v := readConfig(t, "WVA_PASSIVE: true\n")
	if used, discarded := applyLegacyConfigKeys(v, "WVA_"); used != nil || discarded != nil {
		t.Errorf("used = %v, discarded = %v", used, discarded)
	}
}

func TestLookupPrefixed(t *testing.T) {
	const key = "LLMSM_PASSIVE"
	look := func(m map[string]any, k string) (any, bool) { return lookupPrefixed(m, k, "LLMSM_", "WVA_") }

	if v, ok := look(map[string]any{key: true, "WVA_PASSIVE": false}, key); !ok || v != true {
		t.Errorf("the current key must win: %v %v", v, ok)
	}
	if v, ok := look(map[string]any{"WVA_PASSIVE": true}, key); !ok || v != true {
		t.Errorf("the legacy key: %v %v", v, ok)
	}
	if _, ok := look(map[string]any{"PASSIVE": true}, key); ok {
		t.Error("read a key with no prefix at all")
	}
	if _, ok := look(map[string]any{"OTHER": true}, key); ok {
		t.Error("found a key that is not there")
	}
	if _, ok := look(map[string]any{"LLMSM_OTHER": true}, "OTHER"); ok {
		t.Error("a key without the prefix must not fall back")
	}
}

// TestApplyLegacyEnv_RealEnvironment runs on the tree as it stands: it copies
// after the rename and does nothing before it.
func TestApplyLegacyEnv_RealEnvironment(t *testing.T) {
	t.Setenv("WVA_LEGACY_ENV_TEST", "1")
	used, _ := ApplyLegacyEnv()
	copied, ok := os.LookupEnv(EnvPrefix + "LEGACY_ENV_TEST")
	if EnvPrefix == legacyEnvPrefix {
		if used != nil {
			t.Errorf("used = %v before the rename", used)
		}
		return
	}
	t.Cleanup(func() { _ = os.Unsetenv(EnvPrefix + "LEGACY_ENV_TEST") })
	if !ok || copied != "1" {
		t.Errorf("%sLEGACY_ENV_TEST = %q, %v", EnvPrefix, copied, ok)
	}
	if !slices.Contains(used, "WVA_LEGACY_ENV_TEST") {
		t.Errorf("used = %v", used)
	}
}

// TestManagerConfigPassive_ReadsTheOldKey is a ConfigMap written before the
// rename, read by the code after it. Before the rename it is simply the key.
func TestManagerConfigPassive_ReadsTheOldKey(t *testing.T) {
	passive, set := ManagerConfigPassive(map[string]string{ManagerConfigKey: "WVA_PASSIVE: true\n"})
	if !passive || !set {
		t.Errorf("passive=%v set=%v", passive, set)
	}
}

func TestScalingPolicyConfigMapNames_OldNamesAfterTheCurrentOne(t *testing.T) {
	got := ScalingPolicyConfigMapNames()
	if got[0] != ScalingPolicyConfigMapName() {
		t.Errorf("the current name must come first: %v", got)
	}
	if !slices.Contains(got, RenamedScalingPolicyConfigMapName) || !slices.Contains(got, LegacyScalingPolicyConfigMapName) {
		t.Errorf("an old name is missing: %v", got)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("%s appears twice: %v", n, got)
		}
		seen[n] = true
	}
}

func TestScalingPolicyConfigMapNames_AnOverrideDoesNotFallBackToTheRenamedName(t *testing.T) {
	t.Setenv("SCALING_POLICY_CONFIG_MAP_NAME", "my-policy")
	got := ScalingPolicyConfigMapNames()
	if want := []string{"my-policy", LegacyScalingPolicyConfigMapName}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestConfigMapCacheSelector(t *testing.T) {
	sel := ConfigMapCacheSelector("scaling-manager")
	const key = "app.kubernetes.io/name"
	for value, want := range map[string]bool{"scaling-manager": true, legacyAppLabelValue: true, "something-else": false} {
		if got := sel.Matches(labels.Set{key: value}); got != want {
			t.Errorf("%s: matches = %v, want %v", value, got, want)
		}
	}
	if sel.Matches(labels.Set{}) {
		t.Error("a ConfigMap with no label was selected")
	}
	single := ConfigMapCacheSelector(legacyAppLabelValue)
	if !single.Matches(labels.Set{key: legacyAppLabelValue}) || strings.Contains(single.String(), ",") {
		t.Errorf("before the rename the selector should hold one value: %s", single)
	}
}
