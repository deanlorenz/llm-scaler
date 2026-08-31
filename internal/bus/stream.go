// Package bus wraps the NATS JetStream calls agentbus needs: idempotent stream
// setup, publish, and sequence-offset fetch.
package bus

import (
	"context"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	// StreamName is the single JetStream stream that captures all agentbus
	// traffic across all bus IDs and topics.
	StreamName = "AGENTBUS"

	// SubjectPrefix is prepended to every NATS subject.
	SubjectPrefix = "agentbus"

	// SubjectPattern matches all agentbus subjects (all bus IDs, all topics).
	SubjectPattern = "agentbus.>"
)

// Subject returns the NATS subject for a given bus ID and topic.
// topic may contain dots; slashes are not valid in NATS subjects.
func Subject(busID, topic string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectPrefix, busID, topic)
}

// TopicFromSubject extracts the topic portion from a full NATS subject.
// e.g. "agentbus.llmd-scaler.M1.C1" → "M1.C1"
func TopicFromSubject(busID, subject string) string {
	prefix := SubjectPrefix + "." + busID + "."
	return strings.TrimPrefix(subject, prefix)
}

// EnsureStreams creates the AGENTBUS stream if it doesn't already exist.
// Safe to call on every startup.
func EnsureStreams(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     StreamName,
		Subjects: []string{SubjectPattern},
		Storage:  jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("ensure %s stream: %w", StreamName, err)
	}
	return nil
}
