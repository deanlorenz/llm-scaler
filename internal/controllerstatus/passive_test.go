package controllerstatus

import (
	"context"
	"strconv"
	"testing"
)

// passiveDefault carries the WVA_PASSIVE the process read at start, which the
// installer compares with wva-manager-config to tell a restart is due.
func TestPassiveDefaultIsPublished(t *testing.T) {
	w := &writes{}
	p, c := newTestPublisher(t, w)
	p.SetPassiveDefault(true)
	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, c).Data[KeyPassiveDefault]; got != strconv.FormatBool(true) {
		t.Fatalf("passiveDefault = %q, want true", got)
	}
}
