// agentbus-relay is the long-running background daemon for the silent-wake
// mechanism. It subscribes to every mission's message subject on NATS JetStream
// and, for each watched worktree that has declared presence for that mission,
// writes a small marker file the PostToolBatch hook can check with a single
// cheap local read — no network call needed from the hook itself.
//
// Watched worktrees are recorded in ~/.agentbus/watched-worktrees.list, one
// absolute path per line. Sessions append to this file via agentbus_publish_presence
// (the MCP tool already writes a presence JSON under <worktree>/.claude/agentbus/
// presence/<mission>.json — the relay reads those to know which missions each
// worktree cares about).
//
// Marker file written on new message:
//
//	<worktree>/.claude/agentbus/inbox/<mission>.marker
//
// Contents: {"last_seq":<n>,"updated_at":"<RFC3339>"}
//
// The relay never deletes marker files. The hook clears its own cursor; the
// marker just records "highest seq seen." A worktree that no longer exists is
// silently skipped.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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

// watchedWorktreesPath is the file that records every worktree the relay watches.
func watchedWorktreesPath() string {
	if p := os.Getenv("AGENTBUS_WORKTREES_LIST"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentbus", "watched-worktrees.list")
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	nc, err := nats.Connect(natsURL())
	if err != nil {
		log.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("create JetStream context: %v", err)
	}

	if err := bus.EnsureStreams(ctx, js); err != nil {
		log.Fatalf("ensure streams: %v", err)
	}

	log.Printf("agentbus-relay: connected to %s, watching %s", natsURL(), watchedWorktreesPath())

	// Use an ordered push consumer on the wildcard subject so we get every new
	// message for every mission as it arrives.
	cons, err := js.OrderedConsumer(ctx, bus.MsgStreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{bus.MsgSubjectPattern},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		log.Fatalf("create ordered consumer: %v", err)
	}

	cc, err := cons.Consume(func(m jetstream.Msg) {
		if err := handleMsg(m); err != nil {
			log.Printf("relay: error handling message: %v", err)
		}
		// No m.Ack() — ordered consumers use AckNonePolicy.
	})
	if err != nil {
		log.Fatalf("start consume: %v", err)
	}
	defer cc.Stop()

	log.Printf("agentbus-relay: listening for new messages")
	<-ctx.Done()
	log.Printf("agentbus-relay: shutting down")
}

// handleMsg is called for each new NATS message. It extracts the mission name,
// finds every watched worktree that has presence for that mission, and writes
// a marker file in each.
func handleMsg(m jetstream.Msg) error {
	var msg schema.Message
	if err := json.Unmarshal(m.Data(), &msg); err != nil {
		return fmt.Errorf("unmarshal message: %w", err)
	}

	meta, err := m.Metadata()
	if err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}
	seq := meta.Sequence.Stream

	worktrees, err := readWorktrees()
	if err != nil {
		// File not found just means no worktrees registered yet.
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, wt := range worktrees {
		presencePath := filepath.Join(wt, ".claude", "agentbus", "presence", msg.Mission+".json")
		if _, err := os.Stat(presencePath); err != nil {
			// This worktree hasn't declared presence for this mission.
			continue
		}
		if err := writeMarker(wt, msg.Mission, seq, now); err != nil {
			log.Printf("relay: write marker for %s/%s: %v", wt, msg.Mission, err)
		}
	}
	return nil
}

// markerPayload is what the relay writes into each inbox marker file.
type markerPayload struct {
	LastSeq   uint64 `json:"last_seq"`
	UpdatedAt string `json:"updated_at"`
}

func writeMarker(worktree, mission string, seq uint64, updatedAt string) error {
	dir := filepath.Join(worktree, ".claude", "agentbus", "inbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	payload, err := json.Marshal(markerPayload{LastSeq: seq, UpdatedAt: updatedAt})
	if err != nil {
		return err
	}

	path := filepath.Join(dir, mission+".marker")
	// Write to a temp file then rename for atomicity — avoids the hook reading
	// a partially-written marker on a concurrent write.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("write tmp marker: %w", err)
	}
	return os.Rename(tmp, path)
}

// readWorktrees reads the watched-worktrees list file and returns non-blank lines.
func readWorktrees() ([]string, error) {
	data, err := os.ReadFile(watchedWorktreesPath())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}
