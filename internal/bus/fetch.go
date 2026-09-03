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

// FetchSince returns messages for busID/topic with stream sequence > sinceSeq,
// up to limit messages. Optionally filters by kind (empty = no filter).
// Returns immediately with whatever is available.
func FetchSince(ctx context.Context, js jetstream.JetStream, busID, topic string, sinceSeq uint64, limit int, kind string) (FetchResult, error) {
	if limit <= 0 {
		limit = 50
	}

	consumer, err := js.OrderedConsumer(ctx, StreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{Subject(busID, topic)},
		DeliverPolicy:  jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:    sinceSeq + 1,
	})
	if err != nil {
		return FetchResult{}, fmt.Errorf("create consumer for %s/%s: %w", busID, topic, err)
	}

	result := FetchResult{LastSeq: sinceSeq}

	batch, err := consumer.Fetch(limit, jetstream.FetchMaxWait(fetchWait))
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch batch for %s/%s: %w", busID, topic, err)
	}

	for m := range batch.Messages() {
		var msg schema.Message
		if err := json.Unmarshal(m.Data(), &msg); err != nil {
			return FetchResult{}, fmt.Errorf("unmarshal message: %w", err)
		}

		meta, err := m.Metadata()
		if err != nil {
			return FetchResult{}, fmt.Errorf("read message metadata: %w", err)
		}
		msg.Seq = meta.Sequence.Stream

		if kind != "" && msg.Kind != kind {
			// Kind filter: skip but still advance LastSeq to avoid re-fetching.
			if meta.Sequence.Stream > result.LastSeq {
				result.LastSeq = meta.Sequence.Stream
			}
			continue
		}

		result.Messages = append(result.Messages, msg)
		if meta.Sequence.Stream > result.LastSeq {
			result.LastSeq = meta.Sequence.Stream
		}
	}

	if err := batch.Error(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return FetchResult{}, fmt.Errorf("batch error for %s/%s: %w", busID, topic, err)
	}

	return result, nil
}

// FetchBySeq fetches a single message from the stream by its sequence number.
func FetchBySeq(ctx context.Context, js jetstream.JetStream, seq uint64) (schema.Message, error) {
	stream, err := js.Stream(ctx, StreamName)
	if err != nil {
		return schema.Message{}, fmt.Errorf("get stream: %w", err)
	}
	raw, err := stream.GetMsg(ctx, seq)
	if err != nil {
		return schema.Message{}, fmt.Errorf("get message seq=%d: %w", seq, err)
	}
	var msg schema.Message
	if err := json.Unmarshal(raw.Data, &msg); err != nil {
		return schema.Message{}, fmt.Errorf("unmarshal message seq=%d: %w", seq, err)
	}
	msg.Seq = seq
	return msg, nil
}

// fetchWait bounds how long FetchSince blocks when nothing is immediately
// available. Short, since "nothing new" is a valid fast answer.
const fetchWait = 500 * time.Millisecond
