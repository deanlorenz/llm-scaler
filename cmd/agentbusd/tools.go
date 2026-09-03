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

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agentbus_ask_user",
		Description: "Ask the user an interactive question via agentbus dialogue (user.in / user.out) and wait for reply.",
	}, askUserHandler(js, busID))
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
			for _, topic := range subs.Topics {
				cursors[topic] = readCursorUint(busID, args.SessionID, topic)
			}
			result.Session = &sessionStatus{
				SessionID:     args.SessionID,
				Subscriptions: subs.Topics,
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

// --- agentbus_ask_user ---

type askUserArgs struct {
	Prompt         string   `json:"prompt,omitempty" jsonschema:"question / prompt text for the user (required unless previous_seq is set)"`
	FromSession    string   `json:"from_session" jsonschema:"this session's slug/id"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" jsonschema:"max seconds to wait for user reply (default 300; ignored in async mode)"`
	Refs           []string `json:"refs,omitempty" jsonschema:"repo-root-relative doc paths"`
	Async          bool     `json:"async,omitempty" jsonschema:"if true, publish and return immediately; hook surfaces reply when it arrives"`
	PreviousSeq    *uint64  `json:"previous_seq,omitempty" jsonschema:"re-ask: seq of a previous async question; fetches original body and blocks for reply"`
}

type askUserResult struct {
	Reply    string       `json:"reply,omitempty"`
	Seq      uint64       `json:"seq,omitempty"`
	From     *schema.From `json:"from,omitempty"`
	TimedOut bool         `json:"timed_out,omitempty"`
}

func askUserHandler(js jetstream.JetStream, busID string) mcp.ToolHandlerFor[askUserArgs, askUserResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args askUserArgs) (*mcp.CallToolResult, askUserResult, error) {
		if args.Async && args.PreviousSeq != nil {
			return nil, askUserResult{}, fmt.Errorf("async and previous_seq are mutually exclusive")
		}
		if args.Prompt == "" && args.PreviousSeq == nil {
			return nil, askUserResult{}, fmt.Errorf("prompt is required unless previous_seq is set")
		}

		// --- Re-ask / wait mode ---
		if args.PreviousSeq != nil {
			return askUserReask(ctx, js, busID, args)
		}

		// --- Async mode ---
		if args.Async {
			return askUserAsync(ctx, js, busID, args)
		}

		// --- Sync mode (default) ---
		return askUserSync(ctx, js, busID, args)
	}
}

// askUserSync publishes a question and blocks until a reply addressed to
// from_session arrives on user.out, or timeout elapses.
func askUserSync(ctx context.Context, js jetstream.JetStream, busID string, args askUserArgs) (*mcp.CallToolResult, askUserResult, error) {
	timeout := 300 * time.Second
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	replyChan, cc, err := listenForReply(waitCtx, js, busID, args.FromSession)
	if err != nil {
		return nil, askUserResult{}, err
	}
	defer cc.Stop()

	questionSeq, err := publishQuestion(ctx, js, busID, args.FromSession, args.Prompt, args.Refs)
	if err != nil {
		return nil, askUserResult{}, err
	}
	_ = questionSeq

	return waitForReply(waitCtx, replyChan)
}

// askUserAsync publishes a question, registers a filtered subscription on
// user.out, and returns immediately with the question seq.
func askUserAsync(ctx context.Context, js jetstream.JetStream, busID string, args askUserArgs) (*mcp.CallToolResult, askUserResult, error) {
	questionSeq, err := publishQuestion(ctx, js, busID, args.FromSession, args.Prompt, args.Refs)
	if err != nil {
		return nil, askUserResult{}, err
	}

	if err := bus.SubscribeFiltered(busID, args.FromSession, "user.out",
		bus.TopicFilter{Receiver: args.FromSession}); err != nil {
		return nil, askUserResult{}, fmt.Errorf("register async subscription: %w", err)
	}

	return nil, askUserResult{Seq: questionSeq}, nil
}

// askUserReask fetches the original question by seq, re-publishes it with a
// [reminder] prefix, and blocks for a reply addressed to from_session.
func askUserReask(ctx context.Context, js jetstream.JetStream, busID string, args askUserArgs) (*mcp.CallToolResult, askUserResult, error) {
	original, err := bus.FetchBySeq(ctx, js, *args.PreviousSeq)
	if err != nil {
		return nil, askUserResult{}, fmt.Errorf("fetch original question (seq=%d): %w", *args.PreviousSeq, err)
	}

	timeout := 300 * time.Second
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	replyChan, cc, err := listenForReply(waitCtx, js, busID, args.FromSession)
	if err != nil {
		return nil, askUserResult{}, err
	}
	defer cc.Stop()

	reminderBody := "[reminder] " + original.Body
	if _, err := publishQuestion(ctx, js, busID, args.FromSession, reminderBody, original.Refs); err != nil {
		return nil, askUserResult{}, err
	}

	result, res, err := waitForReply(waitCtx, replyChan)
	// Clean up the async filtered subscription now that we have blocked for the reply.
	_ = bus.RemoveFilter(busID, args.FromSession, "user.out")
	return result, res, err
}

// listenForReply creates an ordered consumer on user.out that surfaces only
// messages addressed to fromSession.
func listenForReply(ctx context.Context, js jetstream.JetStream, busID, fromSession string) (<-chan schema.Message, jetstream.ConsumeContext, error) {
	outSubj := bus.Subject(busID, "user.out")
	cons, err := js.OrderedConsumer(ctx, bus.StreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{outSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create reply consumer: %w", err)
	}

	replyChan := make(chan schema.Message, 10)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		var msg schema.Message
		if err := json.Unmarshal(m.Data(), &msg); err != nil {
			return
		}
		if msg.Receiver != "" && msg.Receiver != fromSession {
			return // not for us
		}
		meta, err := m.Metadata()
		if err != nil {
			return
		}
		msg.Seq = meta.Sequence.Stream
		replyChan <- msg
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start reply consumer: %w", err)
	}
	return replyChan, cc, nil
}

// publishQuestion publishes kind=question to user.in and returns its seq.
func publishQuestion(ctx context.Context, js jetstream.JetStream, busID, fromSession, prompt string, refs []string) (uint64, error) {
	return bus.Publish(ctx, js, busID, schema.Message{
		Topic: "user.in",
		From:  schema.From{Agent: agentName(), Session: fromSession},
		TS:    time.Now().UTC().Format(time.RFC3339),
		Kind:  "question",
		Body:  prompt,
		Refs:  refs,
	})
}

// waitForReply blocks on replyChan until a message arrives or ctx is done.
func waitForReply(ctx context.Context, replyChan <-chan schema.Message) (*mcp.CallToolResult, askUserResult, error) {
	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return nil, askUserResult{TimedOut: true, Reply: "Timed out waiting for user reply."}, nil
		}
		return nil, askUserResult{}, ctx.Err()
	case reply := <-replyChan:
		return nil, askUserResult{Reply: reply.Body, Seq: reply.Seq, From: &reply.From}, nil
	}
}
