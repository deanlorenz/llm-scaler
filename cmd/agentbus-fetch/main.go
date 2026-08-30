// agentbus-fetch is a thin CLI for the PostToolBatch hook to call.
// It connects to NATS, fetches messages for a mission since a given sequence,
// prints JSON to stdout, and exits. One-shot, no server lifecycle.
//
// Usage: agentbus-fetch <mission> <since_seq> [limit]
// Output: {"messages":[...],"last_seq":<n>}
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/bus"
)

func natsURL() string {
	if u := os.Getenv("AGENTBUS_NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: agentbus-fetch <mission> <since_seq> [limit]\n")
		os.Exit(1)
	}

	mission := os.Args[1]
	sinceSeq, err := strconv.ParseUint(os.Args[2], 10, 64)
	if err != nil {
		log.Fatalf("invalid since_seq %q: %v", os.Args[2], err)
	}
	limit := 50
	if len(os.Args) >= 4 {
		if n, err := strconv.Atoi(os.Args[3]); err == nil {
			limit = n
		}
	}

	ctx := context.Background()

	nc, err := nats.Connect(natsURL())
	if err != nil {
		log.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("create JetStream context: %v", err)
	}

	res, err := bus.FetchSince(ctx, js, mission, sinceSeq, limit)
	if err != nil {
		log.Fatalf("fetch: %v", err)
	}

	type output struct {
		Messages interface{} `json:"messages"`
		LastSeq  uint64      `json:"last_seq"`
	}
	msgs := res.Messages
	if msgs == nil {
		msgs = nil // keep JSON null vs []
	}
	enc := json.NewEncoder(os.Stdout)
	enc.Encode(output{Messages: msgs, LastSeq: res.LastSeq})
}
