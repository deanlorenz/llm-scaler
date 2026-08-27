// Package bus wraps the NATS JetStream calls agentbus needs: idempotent stream
// setup, publish, and sequence-offset fetch. Both the MCP server (cmd/agentbusd)
// and the wake hook's fetch path import this directly rather than duplicating the
// JetStream calls.
package bus

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	MsgStreamName          = "AGENTBUS"
	MsgSubjectPattern      = "agentbus.mission.*.msg"
	PresenceStreamName     = "AGENTBUS_PRESENCE"
	PresenceSubjectPattern = "agentbus.mission.*.presence"

	// presenceMaxAge bounds AGENTBUS_PRESENCE to "recently active" rather than
	// permanent history — presence is a liveness signal, not an audit log.
	presenceMaxAge = 24 * time.Hour
)

// EnsureStreams creates the AGENTBUS and AGENTBUS_PRESENCE streams if they don't
// already exist, or returns the existing ones. Safe to call on every startup.
func EnsureStreams(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     MsgStreamName,
		Subjects: []string{MsgSubjectPattern},
		Storage:  jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("ensure %s stream: %w", MsgStreamName, err)
	}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     PresenceStreamName,
		Subjects: []string{PresenceSubjectPattern},
		Storage:  jetstream.FileStorage,
		MaxAge:   presenceMaxAge,
	})
	if err != nil {
		return fmt.Errorf("ensure %s stream: %w", PresenceStreamName, err)
	}

	return nil
}

// MsgSubject returns the NATS subject a mission's messages are published to.
func MsgSubject(mission string) string {
	return fmt.Sprintf("agentbus.mission.%s.msg", mission)
}

// PresenceSubject returns the NATS subject a mission's presence announcements are
// published to.
func PresenceSubject(mission string) string {
	return fmt.Sprintf("agentbus.mission.%s.presence", mission)
}
