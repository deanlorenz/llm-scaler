package config

// Passive-mode sources: the layer whose value decided a model's passive mode,
// as ResolvePassive reports it and the "Passive mode" log lines print it.
const (
	// PassiveSourcePerModel is a per-model entry (its body names the model).
	PassiveSourcePerModel = "perModel"
	// PassiveSourceTierPrefix prefixes the name of the tier that decided it.
	PassiveSourceTierPrefix = "tier:"
	// PassiveSourceDefault is the controller's own `default` entry.
	PassiveSourceDefault = "default"
	// PassiveSourceNamespaceDefault is the `default` entry of the model
	// namespace's own scaling-policy map, which replaces the controller's.
	PassiveSourceNamespaceDefault = "namespaceDefault"
	// PassiveSourceFlag is the WVA_PASSIVE controller flag.
	PassiveSourceFlag = "WVA_PASSIVE"
)

// ResolvePassive reports whether a model is passive -- observed, never scaled
// -- and the layer that decided it.
//
// tiers is the tier each of the model's variants names in its trigger
// metadata, "" for none (ModelTiers normalises it). The layers, most specific
// first:
//
//  1. a per-model entry that sets `passive` (FindModelOverride);
//  2. the tier the variant names, else `default.defaultPolicy`;
//  3. the `default` entry -- the namespace's own map when it has one, which
//     replaces the controller's whole;
//  4. the WVA_PASSIVE flag.
//
// When the variants name different tiers the model is passive if ANY of them
// resolves passive: a model is scaled whole, and passive is the side that
// moves nothing. A per-model entry sits above every tier, so its `passive:
// false` makes the whole model active.
//
// modelID "" (a warm pool's trigger, which serves no model) skips layer 1.
// A nil cfg is active, from the flag's default.
func ResolvePassive(cfg *Config, namespace, modelID string, tiers []string) (bool, string) {
	if cfg == nil {
		return false, PassiveSourceFlag
	}
	policies := cfg.ScalingPolicyConfigForNamespace(namespace)
	if modelID != "" {
		if o, ok := FindModelOverride(policies, modelID, namespace); ok && o.Passive != nil {
			return *o.Passive, PassiveSourcePerModel
		}
	}
	base, baseSource := cfg.PassiveDefault(), PassiveSourceFlag
	def, hasDefault := policies[GlobalDefaultsKey]
	if hasDefault && def.Passive != nil {
		base, baseSource = *def.Passive, PassiveSourceDefault
		if cfg.NamespaceHasLocalPolicy(namespace) {
			baseSource = PassiveSourceNamespaceDefault
		}
	}
	first, firstSource := false, ""
	for i, tier := range ModelTiers(tiers) {
		if tier == "" {
			tier = def.DefaultPolicy
		}
		v, src := base, baseSource
		if entry, ok := policies[tier]; ok && tier != "" && PolicyEntryKey(tier, entry) && entry.Passive != nil {
			v, src = *entry.Passive, PassiveSourceTierPrefix+tier
		}
		if v {
			return true, src
		}
		if i == 0 {
			first, firstSource = v, src
		}
	}
	return first, firstSource
}
