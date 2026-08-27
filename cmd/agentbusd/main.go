// agentbusd is the agentbus MCP server: a thin, tool-agnostic layer over a local
// NATS JetStream instance. It exposes four tools — publish, fetch-since,
// publish-presence, list-missions — and holds no server-side consumer/cursor
// state; callers persist their own read cursor.
package main

import (
	"context"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	if err := bus.EnsureStreams(ctx, js); err != nil {
		log.Fatalf("ensure streams: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "agentbus", Version: "0.1.0"}, nil)
	registerTools(server, js)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server run: %v", err)
	}
}
