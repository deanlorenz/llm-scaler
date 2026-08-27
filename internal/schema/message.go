// Package schema defines the plain, tool-agnostic message format published to and
// read from agentbus subjects. It intentionally has no Claude-specific fields — any
// MCP-capable tool (Claude Code, Bob, or otherwise) can produce and consume it.
package schema

// From identifies the sender of a Message. Agent is a free string ("claude-code",
// "bob", or anything else) — never a fixed enum, so a new tool never needs a schema
// change to participate.
type From struct {
	Agent   string `json:"agent"`
	Session string `json:"session"`
}

// Message is the payload published to an agentbus.mission.<slug>.msg subject.
//
// There is no separate identity field: the JetStream stream sequence number
// returned by a fetch call serves as the message's identity.
type Message struct {
	Mission string   `json:"mission"`
	From    From     `json:"from"`
	TS      string   `json:"ts"` // RFC 3339
	Kind    string   `json:"kind,omitempty"`
	ReplyTo *uint64  `json:"reply_to,omitempty"` // sequence number of the message this replies to
	Body    string   `json:"body"`
	Refs    []string `json:"refs,omitempty"` // repo-root-relative doc paths
}

// Presence is the payload published to an agentbus.mission.<slug>.presence subject.
type Presence struct {
	Mission  string `json:"mission"`
	From     From   `json:"from"`
	Worktree string `json:"worktree"`
	Since    string `json:"since"` // RFC 3339
}
