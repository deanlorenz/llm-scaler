package bus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/schema"
)

// Publish sends msg to its mission's subject and returns the stream sequence
// number JetStream assigned it — this is the message's identity for later replay.
func Publish(ctx context.Context, js jetstream.JetStream, msg schema.Message) (uint64, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return 0, fmt.Errorf("marshal message: %w", err)
	}

	ack, err := js.Publish(ctx, MsgSubject(msg.Mission), payload)
	if err != nil {
		return 0, fmt.Errorf("publish to %s: %w", MsgSubject(msg.Mission), err)
	}

	return ack.Sequence, nil
}

// PublishPresence announces a session's interest in a mission.
func PublishPresence(ctx context.Context, js jetstream.JetStream, p schema.Presence) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal presence: %w", err)
	}

	_, err = js.Publish(ctx, PresenceSubject(p.Mission), payload)
	if err != nil {
		return fmt.Errorf("publish to %s: %w", PresenceSubject(p.Mission), err)
	}

	return nil
}
