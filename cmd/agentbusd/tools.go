package main

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/bus"
	"github.com/deanlorenz/agentbus/internal/schema"
)

func registerTools(server *mcp.Server, js jetstream.JetStream) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_publish",
		Description: "Publish a message to a mission's topic.",
	}, publishHandler(js))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_fetch_since",
		Description: "Fetch messages for a mission newer than a given sequence number. Returns immediately; an empty result means nothing new.",
	}, fetchSinceHandler(js))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_publish_presence",
		Description: "Announce this session's interest in a mission, for discovery by other agents.",
	}, publishPresenceHandler(js))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_list_missions",
		Description: "List missions with recent presence activity.",
	}, listMissionsHandler(js))
}

// --- agentbus_publish ---

type publishArgs struct {
	Mission     string   `json:"mission" jsonschema:"the mission/topic name"`
	FromAgent   string   `json:"from_agent" jsonschema:"free-text tool identity, e.g. claude-code or bob"`
	FromSession string   `json:"from_session" jsonschema:"the sending session's own slug/id"`
	Kind        string   `json:"kind,omitempty" jsonschema:"open vocabulary, e.g. note, question, handoff, ack"`
	Body        string   `json:"body" jsonschema:"the message text"`
	ReplyTo     *uint64  `json:"reply_to,omitempty" jsonschema:"sequence number of the message this replies to"`
	Refs        []string `json:"refs,omitempty" jsonschema:"repo-root-relative doc paths this message references"`
}

type publishResult struct {
	Seq uint64 `json:"seq"`
}

func publishHandler(js jetstream.JetStream) mcp.ToolHandlerFor[publishArgs, publishResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args publishArgs) (*mcp.CallToolResult, publishResult, error) {
		seq, err := bus.Publish(ctx, js, schema.Message{
			Mission: args.Mission,
			From:    schema.From{Agent: args.FromAgent, Session: args.FromSession},
			TS:      time.Now().UTC().Format(time.RFC3339),
			Kind:    args.Kind,
			ReplyTo: args.ReplyTo,
			Body:    args.Body,
			Refs:    args.Refs,
		})
		if err != nil {
			return nil, publishResult{}, err
		}
		return nil, publishResult{Seq: seq}, nil
	}
}

// --- agentbus_fetch_since ---

type fetchSinceArgs struct {
	Mission  string `json:"mission" jsonschema:"the mission/topic name"`
	SinceSeq uint64 `json:"since_seq" jsonschema:"return messages with a sequence number greater than this"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum messages to return, default 100"`
}

type fetchSinceResult struct {
	Messages []schema.Message `json:"messages"`
	LastSeq  uint64           `json:"last_seq"`
}

func fetchSinceHandler(js jetstream.JetStream) mcp.ToolHandlerFor[fetchSinceArgs, fetchSinceResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args fetchSinceArgs) (*mcp.CallToolResult, fetchSinceResult, error) {
		res, err := bus.FetchSince(ctx, js, args.Mission, args.SinceSeq, args.Limit)
		if err != nil {
			return nil, fetchSinceResult{}, err
		}
		return nil, fetchSinceResult{Messages: res.Messages, LastSeq: res.LastSeq}, nil
	}
}

// --- agentbus_publish_presence ---

type publishPresenceArgs struct {
	Mission     string `json:"mission" jsonschema:"the mission/topic name"`
	FromAgent   string `json:"from_agent" jsonschema:"free-text tool identity, e.g. claude-code or bob"`
	FromSession string `json:"from_session" jsonschema:"the sending session's own slug/id"`
	Worktree    string `json:"worktree" jsonschema:"absolute path of the worktree this session is running in"`
}

type publishPresenceResult struct{}

func publishPresenceHandler(js jetstream.JetStream) mcp.ToolHandlerFor[publishPresenceArgs, publishPresenceResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args publishPresenceArgs) (*mcp.CallToolResult, publishPresenceResult, error) {
		err := bus.PublishPresence(ctx, js, schema.Presence{
			Mission:  args.Mission,
			From:     schema.From{Agent: args.FromAgent, Session: args.FromSession},
			Worktree: args.Worktree,
			Since:    time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return nil, publishPresenceResult{}, err
		}
		return nil, publishPresenceResult{}, nil
	}
}

// --- agentbus_list_missions ---

type listMissionsArgs struct{}

type missionInfo struct {
	Mission        string   `json:"mission"`
	LastSeen       string   `json:"last_seen"`
	ActiveSessions []string `json:"active_sessions"`
}

type listMissionsResult struct {
	Missions []missionInfo `json:"missions"`
}

func listMissionsHandler(js jetstream.JetStream) mcp.ToolHandlerFor[listMissionsArgs, listMissionsResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ listMissionsArgs) (*mcp.CallToolResult, listMissionsResult, error) {
		missions, err := bus.ListMissions(ctx, js)
		if err != nil {
			return nil, listMissionsResult{}, err
		}

		out := make([]missionInfo, 0, len(missions))
		for _, m := range missions {
			out = append(out, missionInfo{
				Mission:        m.Mission,
				LastSeen:       m.LastSeen,
				ActiveSessions: m.ActiveSessions,
			})
		}
		return nil, listMissionsResult{Missions: out}, nil
	}
}
