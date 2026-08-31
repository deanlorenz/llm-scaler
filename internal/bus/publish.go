package bus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/schema"
)

// Publish sends msg to its topic subject and returns the stream sequence
// number JetStream assigned — this is the message's identity for replay.
func Publish(ctx context.Context, js jetstream.JetStream, busID string, msg schema.Message) (uint64, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return 0, fmt.Errorf("marshal message: %w", err)
	}

	subj := Subject(busID, msg.Topic)
	ack, err := js.Publish(ctx, subj, payload)
	if err != nil {
		return 0, fmt.Errorf("publish to %s: %w", subj, err)
	}

	return ack.Sequence, nil
}
