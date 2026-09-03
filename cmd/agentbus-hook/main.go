// agentbus-hook is the PostToolBatch hook binary. Registered once globally in
// ~/.claude/settings.json; fires after every batch of tool calls.
//
// Fast path (common): reads subscription list and markers from ~/.agentbus/,
// exits 0 silently if nothing new (~1ms, no network).
//
// Slow path (new messages): connects to NATS, fetches, emits additionalContext.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/bus"
	"github.com/deanlorenz/agentbus/internal/schema"
)

func natsURL() string {
	if u := os.Getenv("AGENTBUS_NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

func main() {
	var input struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		os.Exit(0)
	}
	if input.SessionID == "" || input.CWD == "" {
		os.Exit(0)
	}

	// Resolve bus ID from the session's working directory.
	busID, err := bus.ResolveBusID(input.CWD)
	if err != nil {
		os.Exit(0) // not a registered project — exit silently
	}

	// Read this session's subscribed topics + filters.
	subs, err := bus.ReadSubs(busID, input.SessionID)
	if err != nil || len(subs.Topics) == 0 {
		os.Exit(0) // no subscriptions — common case
	}

	type pending struct {
		topic   string
		msgs    []schema.Message
		lastSeq uint64
	}
	var found []pending

	for _, topic := range subs.Topics {
		markerSeq := readMarkerSeq(busID, input.SessionID, topic)
		if markerSeq == 0 {
			continue
		}
		cursorSeq := readCursor(busID, input.SessionID, topic)
		if markerSeq <= cursorSeq {
			continue
		}

		msgs, lastSeq, err := fetchNew(busID, topic, cursorSeq)
		if err != nil {
			// Don't advance cursor on fetch error — messages may still be
			// retrievable on the next turn. The marker remains ahead of the
			// cursor so the hook retries next turn.
			continue
		}

		// Apply per-topic receiver filter if present.
		if f, ok := subs.Filters[topic]; ok && f.Receiver != "" {
			var filtered []schema.Message
			for _, m := range msgs {
				if m.Receiver == f.Receiver {
					filtered = append(filtered, m)
				}
			}
			msgs = filtered
		}

		writeCursor(busID, input.SessionID, topic, lastSeq)
		if len(msgs) == 0 {
			continue
		}
		found = append(found, pending{topic, msgs, lastSeq})
	}

	if len(found) == 0 {
		os.Exit(0)
	}


	var all []schema.Message
	for _, p := range found {
		all = append(all, p.msgs...)
	}

	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolBatch",
			"additionalContext": formatContext(all),
		},
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func readMarkerSeq(busID, sessionID, topic string) uint64 {
	data, err := os.ReadFile(bus.MarkerPath(busID, sessionID, topic))
	if err != nil {
		return 0
	}
	var m struct {
		LastSeq uint64 `json:"last_seq"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return 0
	}
	return m.LastSeq
}

func readCursor(busID, sessionID, topic string) uint64 {
	data, err := os.ReadFile(bus.CursorPath(busID, sessionID, topic))
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func writeCursor(busID, sessionID, topic string, seq uint64) {
	path := bus.CursorPath(busID, sessionID, topic)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("hook: writeCursor mkdir %s: %v", path, err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(seq, 10)), 0o644); err != nil {
		log.Printf("hook: writeCursor write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("hook: writeCursor rename %s: %v", path, err)
	}
}

func fetchNew(busID, topic string, sinceSeq uint64) ([]schema.Message, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	nc, err := nats.Connect(natsURL(), nats.Timeout(2*time.Second))
	if err != nil {
		return nil, 0, err
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return nil, 0, err
	}

	res, err := bus.FetchSince(ctx, js, busID, topic, sinceSeq, 50, "")
	if err != nil {
		return nil, 0, err
	}
	return res.Messages, res.LastSeq, nil
}

func formatContext(messages []schema.Message) string {
	var b strings.Builder
	b.WriteString("[agentbus] New messages:\n")
	for _, m := range messages {
		kind := ""
		if m.Kind != "" {
			kind = " [" + m.Kind + "]"
		}
		refs := ""
		if len(m.Refs) > 0 {
			refs = "\n  refs: " + strings.Join(m.Refs, ", ")
		}
		fmt.Fprintf(&b, "  [%s]%s %s/%s on topic=%s:\n  %s%s\n",
			m.TS, kind, m.From.Agent, m.From.Session, m.Topic, m.Body, refs)
	}
	return b.String()
}
