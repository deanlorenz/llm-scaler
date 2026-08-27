// agentbus-relay is the long-running background daemon that makes silent wake
// possible: it subscribes to every mission's message subject on NATS and, for
// each watched worktree that has declared presence for that mission, writes a
// small marker file the PostToolBatch hook can check with a single cheap local
// read — no network call needed from the hook itself.
//
// Skeleton only for now (T4 implements the real subscribe-and-relay loop).
package main

import (
	"log"
	"os"

	"github.com/nats-io/nats.go"
)

func natsURL() string {
	if u := os.Getenv("AGENTBUS_NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

func main() {
	nc, err := nats.Connect(natsURL())
	if err != nil {
		log.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Close()

	log.Printf("agentbus-relay: connected to %s (T4 not yet implemented)", natsURL())
}
