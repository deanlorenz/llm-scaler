package steadystate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
)

// The size trim once dropped a borrow clock with its state, but a previous
// figure was not an entry, so a trim could
// keep a prior whose clock it dropped; restored, that prior was unused again
// and armed at full hold. A prior and its clock are now one record
// (LowerBounds), so they stay or go together, and the trim must not reach the
// caller's maps.
func TestTheTrimKeepsAPriorWithItsClock(t *testing.T) {
	now := time.Now()
	key := "ns|v|m|H100|1|decode|b1|q5"
	s := saturation.LearnedState{
		LowerBounds: map[string]saturation.LowerBoundSnapshot{key: {
			Armed: true, Since: now.Add(-time.Hour), Tick: now, Seen: now,
			Donor: "v", Own: true, DonorK2: 4000, Args: "x",
			Prior: 4000, PriorSince: now.Add(-time.Hour),
		}},
		K2BorrowedSince: map[string]time.Time{key: now.Add(-time.Hour)},
		Config:          map[string]string{"ns/v": "fp"},
		Args:            map[string]string{"ns/v": "args"},
	}
	full, dropped, err := encodeLearnedState(s, now, 1<<20)
	if err != nil || dropped != 0 {
		t.Fatalf("precondition: full encode, dropped=%d err=%v", dropped, err)
	}
	raw, dropped, err := encodeLearnedState(s, now, len(full)-1)
	if err != nil {
		t.Fatalf("trimmed encode: %v", err)
	}
	if dropped == 0 {
		t.Fatal("precondition: the bound dropped an entry")
	}
	var doc learnedStateDoc
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if lb, kept := doc.State.LowerBounds[key]; kept && (lb.Prior == 0 || !lb.Armed) {
		t.Errorf("trimmed apart: kept %+v", lb)
	}
	if _, start := doc.State.K2BorrowedSince[key]; start {
		if _, kept := doc.State.LowerBounds[key]; !kept {
			t.Error("the rollback start was kept without its bound")
		}
	}
	if len(s.LowerBounds) != 1 || len(s.K2BorrowedSince) != 1 {
		t.Errorf("the trim reached the caller's maps: bounds=%d starts=%d", len(s.LowerBounds), len(s.K2BorrowedSince))
	}
}

// Every map of LearnedState is either an entry the size bound can drop, or
// one of the per-variant configuration tags that are kept. A map added to the
// document and to neither list is caught here, rather than growing past the
// bound with nothing able to trim it -- which is how the prior got in.
func TestEveryLearnedStateMapIsTrimmable(t *testing.T) {
	kept := map[string]bool{"Config": true, "Args": true}
	var s saturation.LearnedState
	v := reflect.ValueOf(&s).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		if f.Kind() != reflect.Map {
			continue
		}
		m := reflect.MakeMap(f.Type())
		key := "ns|v|m|H100|1|decode|b1|q5"
		if v.Type().Field(i).Name == "Accelerators" || v.Type().Field(i).Name == "Forgotten" {
			key = "ns/v"
		}
		m.SetMapIndex(reflect.ValueOf(key), reflect.New(f.Type().Elem()).Elem())
		f.Set(m)
	}
	for _, e := range learnedStateEntries(s) {
		e.drop()
	}
	for i := range v.NumField() {
		f, name := v.Field(i), v.Type().Field(i).Name
		if f.Kind() != reflect.Map || kept[name] {
			continue
		}
		if f.Len() != 0 {
			t.Errorf("LearnedState.%s keeps an entry the size bound cannot drop", name)
		}
	}
}
