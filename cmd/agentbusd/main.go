// agentbusd is the agentbus MCP server. It exposes five tools — publish,
// fetch_since, subscribe, unsubscribe, status — over a local NATS JetStream
// instance. The bus ID is resolved once at startup from CLAUDE_PROJECT_DIR
// (or AGENTBUS_CWD) via ~/.agentbus/repos.json.
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

// resolveCWD returns the working directory to use for bus ID resolution.
// Prefers CLAUDE_PROJECT_DIR (set by Claude Code for all MCP servers),
// falls back to AGENTBUS_CWD (for testing), then os.Getwd().
func resolveCWD() string {
	if d := os.Getenv("CLAUDE_PROJECT_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("AGENTBUS_CWD"); d != "" {
		return d
	}
	cwd, _ := os.Getwd()
	return cwd
}

func main() {
	ctx := context.Background()

	busID, err := bus.ResolveBusID(resolveCWD())
	if err != nil {
		log.Fatalf("agentbusd: %v", err)
	}

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

	server := mcp.NewServer(&mcp.Implementation{Name: "agentbus", Version: "0.2.0"}, nil)
	registerTools(server, js, busID)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server run: %v", err)
	}
}
