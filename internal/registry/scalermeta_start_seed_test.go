package registry

import (
	"testing"
)

// TestReplicaStartSecondsIsParsedAndABadValueIgnored: the seed is read as a
// plain decimal number of seconds in [1, 3600]. Unlike defaultOutputTokens, a bad
// value never rejects the trigger -- before it was parsed every value was
// accepted, and a rejected trigger freezes the model. It is ignored, as if
// unset, and handed back as written so the caller can say so.
func TestReplicaStartSecondsIsParsedAndABadValueIgnored(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		set     bool
		want    float64
		ignored bool
	}{
		{name: "absent"},
		{name: "written empty is absent", raw: "", set: true},
		{name: "a whole number", raw: "120", set: true, want: 120},
		{name: "a fraction", raw: "15.5", set: true, want: 15.5},
		{name: "the hour itself", raw: "3600", set: true, want: 3600},
		{name: "over the hour", raw: "3600.5", set: true, ignored: true},
		{name: "zero", raw: "0", set: true, ignored: true},
		{name: "one second", raw: "1", set: true, want: 1},
		{name: "under a second", raw: "0.999", set: true, ignored: true},
		{name: "an exponent", raw: "1e2", set: true, want: 100},
		{name: "a leading space", raw: " 120", set: true, ignored: true},
		{name: "a trailing space", raw: "120 ", set: true, ignored: true},
		{name: "hex", raw: "0x1p6", set: true, ignored: true},
		{name: "an underscore", raw: "1_20", set: true, ignored: true},
		{name: "negative", raw: "-30", set: true, ignored: true},
		{name: "NaN", raw: "NaN", set: true, ignored: true},
		{name: "infinite", raw: "+Inf", set: true, ignored: true},
		{name: "garbage", raw: "two minutes", set: true, ignored: true},
		{name: "a duration is not seconds", raw: "120s", set: true, ignored: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := map[string]string{ModelIDKey: "m"}
			if tc.set {
				md[ReplicaStartSecondsKey] = tc.raw
			}
			m, err := ParseMeta(md)
			if err != nil {
				t.Fatalf("a replicaStartSeconds of %q must never reject the trigger: %v", tc.raw, err)
			}
			if m.ModelID != "m" {
				t.Errorf("the rest of the trigger must still be read, modelID %q", m.ModelID)
			}
			if m.ReplicaStartSeconds != tc.want {
				t.Errorf("seed: have %v, want %v", m.ReplicaStartSeconds, tc.want)
			}
			wantIgnored := ""
			if tc.ignored {
				wantIgnored = tc.raw
			}
			if m.ReplicaStartSecondsIgnored != wantIgnored {
				t.Errorf("ignored: have %q, want %q", m.ReplicaStartSecondsIgnored, wantIgnored)
			}
		})
	}
}

// TestAPoolTriggerCarriesNoStartSeed: a pool trigger returns before any model
// key is read, the start seed included.
func TestAPoolTriggerCarriesNoStartSeed(t *testing.T) {
	m, err := ParseMeta(map[string]string{WarmPoolNameKey: "shared", ReplicaStartSecondsKey: "120"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ReplicaStartSeconds != 0 || m.ReplicaStartSecondsIgnored != "" {
		t.Errorf("a pool trigger must carry no start seed, have %v / %q", m.ReplicaStartSeconds, m.ReplicaStartSecondsIgnored)
	}
}
