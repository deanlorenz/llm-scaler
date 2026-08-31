// Package schema defines the plain, tool-agnostic message format published to
// and read from agentbus topics. No agent-specific fields — any MCP-capable
// tool (Claude Code, Bob, or otherwise) can produce and consume it.
package schema

// From identifies the sender of a Message. Agent is a free string
// ("claude-code", "bob", …) — never a fixed enum.
type From struct {
	Agent   string `json:"agent"`
	Session string `json:"session"`
}

// Message is the payload published to an agentbus topic subject.
// Seq is the JetStream stream sequence number — assigned on publish, returned
// to the caller, and used as the message's identity for cursor/replay.
type Message struct {
	Topic   string   `json:"topic"`
	From    From     `json:"from"`
	TS      string   `json:"ts"`             // RFC 3339
	Kind    string   `json:"kind,omitempty"` // open vocabulary: note, question, handoff, announce, presence, heartbeat, …
	Seq     uint64   `json:"seq,omitempty"`  // filled in by FetchSince, not by the publisher
	ReplyTo *uint64  `json:"reply_to,omitempty"`
	Body    string   `json:"body"`
	Refs    []string `json:"refs,omitempty"` // repo-root-relative doc paths
}
