// agentbus-relay subscribes to all agentbus traffic on NATS and writes
// marker files to ~/.agentbus/markers/ so the PostToolBatch hook can detect
// new messages with a cheap local file read on every turn.
//
// For each incoming message the relay reads ~/.agentbus/subs/<bus_id>/ to
// find every session subscribed to that topic, then writes/overwrites:
//
//	~/.agentbus/markers/<bus_id>/<session_id>/<topic>.marker
//
// The relay never deletes marker files. The hook manages its own cursors.
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

func agentbusHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentbus")
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

	log.Printf("agentbus-relay: connected to %s", natsURL())

	cons, err := js.OrderedConsumer(ctx, bus.StreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{bus.SubjectPattern},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		log.Fatalf("create ordered consumer: %v", err)
	}

	cc, err := cons.Consume(func(m jetstream.Msg) {
		if err := handleMsg(m); err != nil {
			log.Printf("relay: error handling message: %v", err)
		}
	})
	if err != nil {
		log.Fatalf("start consume: %v", err)
	}
	defer cc.Stop()

	log.Printf("agentbus-relay: listening for new messages")
	<-ctx.Done()
	log.Printf("agentbus-relay: shutting down")
}

// handleMsg processes one incoming NATS message: finds subscribed sessions
// for its topic and writes marker files for each.
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

	// Extract bus_id from the NATS subject: "agentbus.<bus_id>.<topic...>"
	parts := strings.SplitN(m.Subject(), ".", 3)
	if len(parts) < 3 {
		return nil
	}
	busID := parts[1]

	// Find all sessions subscribed to this topic.
	subsDir := filepath.Join(agentbusHome(), "subs", busID)
	entries, err := os.ReadDir(subsDir)
	if err != nil {
		return nil // no subscriptions yet
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".json")

		topics, err := bus.ReadSubs(busID, sessionID)
		if err != nil {
			continue
		}
		for _, t := range topics {
			if t == msg.Topic {
				if err := writeMarker(busID, sessionID, msg.Topic, seq, now); err != nil {
					log.Printf("relay: write marker %s/%s/%s: %v", busID, sessionID, msg.Topic, err)
				}
			}
		}
	}
	return nil
}

type markerPayload struct {
	LastSeq   uint64 `json:"last_seq"`
	UpdatedAt string `json:"updated_at"`
}

func writeMarker(busID, sessionID, topic string, seq uint64, updatedAt string) error {
	path := bus.MarkerPath(busID, sessionID, topic)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(markerPayload{LastSeq: seq, UpdatedAt: updatedAt})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
