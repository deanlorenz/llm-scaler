package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/bus"
	"github.com/deanlorenz/agentbus/internal/schema"
)

func registerTools(server *mcp.Server, js jetstream.JetStream, busID string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_publish",
		Description: "Publish a message to a topic.",
	}, publishHandler(js, busID))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_fetch_since",
		Description: "Fetch messages on a topic newer than a given sequence number. Returns immediately; empty means nothing new. Optional kind filter.",
	}, fetchSinceHandler(js, busID))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_subscribe",
		Description: "Register this session as a watcher of a topic. The PostToolBatch hook will surface new messages on this topic automatically.",
	}, subscribeHandler(busID))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_unsubscribe",
		Description: "Stop watching a topic.",
	}, unsubscribeHandler(busID))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_status",
		Description: "List all topics with recent activity on this bus. If session_id is provided, also return that session's subscriptions and per-topic cursor values.",
	}, statusHandler(js, busID))
}

// --- agentbus_publish ---

type publishArgs struct {
	Topic       string   `json:"topic" jsonschema:"destination topic (short name, no bus_id prefix)"`
	FromSession string   `json:"from_session" jsonschema:"this session's slug/id"`
	Kind        string   `json:"kind,omitempty" jsonschema:"open vocabulary: note, question, handoff, announce, presence, heartbeat, …"`
	Body        string   `json:"body" jsonschema:"message text"`
	ReplyTo     *uint64  `json:"reply_to,omitempty" jsonschema:"seq of the message being replied to"`
	Refs        []string `json:"refs,omitempty" jsonschema:"repo-root-relative doc paths"`
}

type publishResult struct {
	Seq uint64 `json:"seq"`
}

func publishHandler(js jetstream.JetStream, busID string) mcp.ToolHandlerFor[publishArgs, publishResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args publishArgs) (*mcp.CallToolResult, publishResult, error) {
		seq, err := bus.Publish(ctx, js, busID, schema.Message{
			Topic:   args.Topic,
			From:    schema.From{Agent: agentName(), Session: args.FromSession},
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

// agentName returns the agent identity string for the From field.
func agentName() string {
	if a := os.Getenv("AGENTBUS_AGENT_NAME"); a != "" {
		return a
	}
	return "unknown"
}

// --- agentbus_fetch_since ---

type fetchSinceArgs struct {
	Topic    string `json:"topic" jsonschema:"topic to read from"`
	SinceSeq uint64 `json:"since_seq" jsonschema:"return messages with seq greater than this"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max messages to return, default 50"`
	Kind     string `json:"kind,omitempty" jsonschema:"if set, only return messages of this kind"`
}

type fetchSinceResult struct {
	Messages []schema.Message `json:"messages"`
	LastSeq  uint64           `json:"last_seq"`
}

func fetchSinceHandler(js jetstream.JetStream, busID string) mcp.ToolHandlerFor[fetchSinceArgs, fetchSinceResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args fetchSinceArgs) (*mcp.CallToolResult, fetchSinceResult, error) {
		res, err := bus.FetchSince(ctx, js, busID, args.Topic, args.SinceSeq, args.Limit, args.Kind)
		if err != nil {
			return nil, fetchSinceResult{}, err
		}
		return nil, fetchSinceResult{Messages: res.Messages, LastSeq: res.LastSeq}, nil
	}
}

// --- agentbus_subscribe ---

type subscribeArgs struct {
	Topic     string `json:"topic" jsonschema:"topic to watch"`
	SessionID string `json:"session_id" jsonschema:"this session's id"`
}

type subscribeResult struct{}

func subscribeHandler(busID string) mcp.ToolHandlerFor[subscribeArgs, subscribeResult] {
	return func(_ context.Context, _ *mcp.CallToolRequest, args subscribeArgs) (*mcp.CallToolResult, subscribeResult, error) {
		if err := bus.Subscribe(busID, args.SessionID, args.Topic); err != nil {
			return nil, subscribeResult{}, err
		}
		return nil, subscribeResult{}, nil
	}
}

// --- agentbus_unsubscribe ---

type unsubscribeArgs struct {
	Topic     string `json:"topic" jsonschema:"topic to stop watching"`
	SessionID string `json:"session_id" jsonschema:"this session's id"`
}

type unsubscribeResult struct{}

func unsubscribeHandler(busID string) mcp.ToolHandlerFor[unsubscribeArgs, unsubscribeResult] {
	return func(_ context.Context, _ *mcp.CallToolRequest, args unsubscribeArgs) (*mcp.CallToolResult, unsubscribeResult, error) {
		if err := bus.Unsubscribe(busID, args.SessionID, args.Topic); err != nil {
			return nil, unsubscribeResult{}, err
		}
		return nil, unsubscribeResult{}, nil
	}
}

// --- agentbus_status ---

type statusArgs struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"if set, also return this session's subscriptions and cursors"`
}

type topicInfo struct {
	Topic      string `json:"topic"`
	LastSeq    uint64 `json:"last_seq"`
	LastTS     string `json:"last_ts,omitempty"`
	LastSender string `json:"last_sender,omitempty"`
}

type sessionStatus struct {
	SessionID     string            `json:"session_id"`
	Subscriptions []string          `json:"subscriptions"`
	Cursors       map[string]uint64 `json:"cursors"`
}

type statusResult struct {
	BusID   string         `json:"bus_id"`
	Topics  []topicInfo    `json:"topics"`
	Session *sessionStatus `json:"session,omitempty"`
}

func statusHandler(js jetstream.JetStream, busID string) mcp.ToolHandlerFor[statusArgs, statusResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args statusArgs) (*mcp.CallToolResult, statusResult, error) {
		result := statusResult{BusID: busID}

		// Get stream info with subject-level counts.
		stream, err := js.Stream(ctx, bus.StreamName)
		if err != nil {
			return nil, statusResult{}, fmt.Errorf("get stream: %w", err)
		}
		prefix := bus.SubjectPrefix + "." + busID + "."
		info, err := stream.Info(ctx, jetstream.WithSubjectFilter(prefix+">"))
		if err != nil {
			return nil, statusResult{}, fmt.Errorf("get stream info: %w", err)
		}

		for subj := range info.State.Subjects {
			topic := bus.TopicFromSubject(busID, subj)
			if topic == "" {
				continue
			}
			// Get last message for this subject to extract ts and sender.
			raw, err := stream.GetLastMsgForSubject(ctx, subj)
			ti := topicInfo{Topic: topic}
			if err == nil {
				ti.LastTS = raw.Time.UTC().Format(time.RFC3339)
				// Parse seq from the raw message metadata.
				// raw.Sequence is the stream sequence.
				ti.LastSeq = raw.Sequence
				// Parse sender from payload.
				var msg schema.Message
				if jsonErr := parseJSON(raw.Data, &msg); jsonErr == nil {
					ti.LastSender = msg.From.Session
				}
			}
			result.Topics = append(result.Topics, ti)
		}
		sort.Slice(result.Topics, func(i, j int) bool {
			return result.Topics[i].Topic < result.Topics[j].Topic
		})

		// Optional session detail.
		if args.SessionID != "" {
			subs, _ := bus.ReadSubs(busID, args.SessionID)
			cursors := make(map[string]uint64)
			for _, topic := range subs {
				cursors[topic] = readCursorUint(busID, args.SessionID, topic)
			}
			result.Session = &sessionStatus{
				SessionID:     args.SessionID,
				Subscriptions: subs,
				Cursors:       cursors,
			}
		}

		return nil, result, nil
	}
}

// readCursorUint reads a cursor file and returns the uint64 value, or 0.
func readCursorUint(busID, sessionID, topic string) uint64 {
	path := bus.CursorPath(busID, sessionID, topic)
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

func parseJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
