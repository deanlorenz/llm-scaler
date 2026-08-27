package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/schema"
)

// FetchResult is the outcome of a FetchSince call.
type FetchResult struct {
	Messages []schema.Message
	LastSeq  uint64
}

// FetchSince returns messages for mission with a stream sequence number greater
// than sinceSeq, up to limit messages. The caller owns its own read cursor (a
// plain integer it persists itself) — this call never creates server-side durable
// consumer state, keeping agentbusd free of consumer lifecycle management.
//
// Returns immediately with whatever is available; an empty result means nothing
// new, not an error.
func FetchSince(ctx context.Context, js jetstream.JetStream, mission string, sinceSeq uint64, limit int) (FetchResult, error) {
	if limit <= 0 {
		limit = 100
	}

	consumer, err := js.OrderedConsumer(ctx, MsgStreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{MsgSubject(mission)},
		DeliverPolicy:  jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:    sinceSeq + 1,
	})
	if err != nil {
		return FetchResult{}, fmt.Errorf("create consumer for mission %s: %w", mission, err)
	}

	result := FetchResult{LastSeq: sinceSeq}

	batch, err := consumer.Fetch(limit, jetstream.FetchMaxWait(fetchWait))
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch batch for mission %s: %w", mission, err)
	}

	for m := range batch.Messages() {
		var msg schema.Message
		if err := json.Unmarshal(m.Data(), &msg); err != nil {
			return FetchResult{}, fmt.Errorf("unmarshal message: %w", err)
		}
		result.Messages = append(result.Messages, msg)

		meta, err := m.Metadata()
		if err != nil {
			return FetchResult{}, fmt.Errorf("read message metadata: %w", err)
		}
		if meta.Sequence.Stream > result.LastSeq {
			result.LastSeq = meta.Sequence.Stream
		}
		// No m.Ack(): ordered consumers use AckNonePolicy, and the read cursor
		// here is caller-held (sinceSeq), not server-managed.
	}

	if err := batch.Error(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return FetchResult{}, fmt.Errorf("batch error for mission %s: %w", mission, err)
	}

	return result, nil
}

// fetchWait bounds how long a FetchSince call blocks waiting for a pull-consumer
// batch when nothing new is immediately available. Short, since FetchSince is
// meant to return promptly ("nothing new" is a valid, fast answer), not to act as
// a long-poll.
const fetchWait = 500 * time.Millisecond
