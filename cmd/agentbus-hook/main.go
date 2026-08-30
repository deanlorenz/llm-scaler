// agentbus-hook is the PostToolBatch hook binary. Claude Code runs it after
// every batch of tool calls, passing event data as JSON on stdin. The hook:
//
//  1. Reads cwd and session_id from stdin.
//  2. Checks whether this worktree has declared presence for any mission
//     (<cwd>/.claude/agentbus/presence/*.json). If not, exits 0 silently.
//  3. For each mission, compares the relay-written marker file
//     (<cwd>/.claude/agentbus/inbox/<mission>.marker) against this session's
//     own cursor (<cwd>/.claude/agentbus/consumer/<session>.<mission>.cursor).
//  4. If the marker shows a higher sequence: connects to NATS, fetches new
//     messages, writes additionalContext JSON to stdout, updates the cursor.
//  5. If nothing is new, exits 0 with no output — the common case.
//
// Register once globally in ~/.claude/settings.json:
//
//	{"hooks":{"PostToolBatch":[{"hooks":[{"type":"command","command":"agentbus-hook","timeout":10}]}]}}
package main

import (
	"context"
	"encoding/json"
	"fmt"
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

const (
	presenceDir = ".claude/agentbus/presence"
	inboxDir    = ".claude/agentbus/inbox"
	consumerDir = ".claude/agentbus/consumer"
)

func main() {
	// Read hook input from stdin.
	var input struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		os.Exit(0) // malformed input — exit silently, never disrupt the agent
	}
	if input.CWD == "" {
		os.Exit(0)
	}

	// Check for declared missions in this worktree.
	missions := declaredMissions(input.CWD)
	if len(missions) == 0 {
		os.Exit(0) // common case: this worktree has no mission — done in ~1ms
	}

	// For each mission, check marker vs cursor.
	type newMsg struct {
		mission string
		msgs    []schema.Message
		lastSeq uint64
	}
	var pending []newMsg

	for _, mission := range missions {
		markerSeq := readMarkerSeq(input.CWD, mission)
		if markerSeq == 0 {
			continue // relay hasn't written a marker yet
		}
		cursorSeq := readCursor(input.CWD, input.SessionID, mission)
		if markerSeq <= cursorSeq {
			continue // nothing new
		}

		// Fetch from NATS — only reached when relay says something arrived.
		msgs, lastSeq, err := fetchNew(mission, cursorSeq)
		if err != nil || len(msgs) == 0 {
			// Advance cursor to marker to avoid retrying a transient failure
			// forever. If fetch truly failed, the message is still in NATS.
			writeCursor(input.CWD, input.SessionID, mission, markerSeq)
			continue
		}
		pending = append(pending, newMsg{mission, msgs, lastSeq})
	}

	if len(pending) == 0 {
		os.Exit(0)
	}

	// Update cursors.
	for _, p := range pending {
		writeCursor(input.CWD, input.SessionID, p.mission, p.lastSeq)
	}

	// Emit additionalContext.
	var all []schema.Message
	for _, p := range pending {
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

// declaredMissions returns the mission slugs this worktree has presence for.
func declaredMissions(cwd string) []string {
	dir := filepath.Join(cwd, presenceDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var missions []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			missions = append(missions, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	return missions
}

// readMarkerSeq reads last_seq from the relay-written marker file.
func readMarkerSeq(cwd, mission string) uint64 {
	path := filepath.Join(cwd, inboxDir, mission+".marker")
	data, err := os.ReadFile(path)
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

// readCursor returns this session's last-seen sequence for mission.
func readCursor(cwd, sessionID, mission string) uint64 {
	path := filepath.Join(cwd, consumerDir, sessionID+"."+mission+".cursor")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// writeCursor atomically updates the cursor file.
func writeCursor(cwd, sessionID, mission string, seq uint64) {
	dir := filepath.Join(cwd, consumerDir)
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, sessionID+"."+mission+".cursor")
	tmp := path + ".tmp"
	os.WriteFile(tmp, []byte(strconv.FormatUint(seq, 10)), 0o644)
	os.Rename(tmp, path)
}

// fetchNew connects to NATS and returns messages newer than sinceSeq.
func fetchNew(mission string, sinceSeq uint64) ([]schema.Message, uint64, error) {
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

	res, err := bus.FetchSince(ctx, js, mission, sinceSeq, 50)
	if err != nil {
		return nil, 0, err
	}
	return res.Messages, res.LastSeq, nil
}

// formatContext formats messages into the additionalContext string.
func formatContext(messages []schema.Message) string {
	var b strings.Builder
	b.WriteString("[agentbus] New messages on your mission:\n")
	for _, m := range messages {
		kind := ""
		if m.Kind != "" {
			kind = " [" + m.Kind + "]"
		}
		refs := ""
		if len(m.Refs) > 0 {
			refs = "\n  refs: " + strings.Join(m.Refs, ", ")
		}
		fmt.Fprintf(&b, "  [%s]%s %s/%s on mission=%s:\n  %s%s\n",
			m.TS, kind, m.From.Agent, m.From.Session, m.Mission, m.Body, refs)
	}
	return b.String()
}
