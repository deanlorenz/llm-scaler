package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/schema"
)

// MissionSummary is one entry in a ListMissions result.
type MissionSummary struct {
	Mission        string
	LastSeen       string
	ActiveSessions []string
}

// ListMissions returns a summary of every mission with a presence announcement
// on the AGENTBUS_PRESENCE stream. This is a single bounded Info call plus one
// GetLastMsgForSubject per distinct mission subject found — never a live
// subscription.
func ListMissions(ctx context.Context, js jetstream.JetStream) ([]MissionSummary, error) {
	stream, err := js.Stream(ctx, PresenceStreamName)
	if err != nil {
		return nil, fmt.Errorf("get stream %s: %w", PresenceStreamName, err)
	}

	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(PresenceSubjectPattern))
	if err != nil {
		return nil, fmt.Errorf("get info for %s: %w", PresenceStreamName, err)
	}

	summaries := make([]MissionSummary, 0, len(info.State.Subjects))
	for subject := range info.State.Subjects {
		mission := missionFromPresenceSubject(subject)
		if mission == "" {
			continue
		}

		raw, err := stream.GetLastMsgForSubject(ctx, subject)
		if err != nil {
			return nil, fmt.Errorf("get last presence message for %s: %w", subject, err)
		}

		var p schema.Presence
		if err := json.Unmarshal(raw.Data, &p); err != nil {
			return nil, fmt.Errorf("unmarshal presence for %s: %w", subject, err)
		}

		summaries = append(summaries, MissionSummary{
			Mission:        mission,
			LastSeen:       p.Since,
			ActiveSessions: []string{p.From.Session},
		})
	}

	return summaries, nil
}

// missionFromPresenceSubject extracts <slug> from "agentbus.mission.<slug>.presence".
func missionFromPresenceSubject(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 || parts[0] != "agentbus" || parts[1] != "mission" || parts[3] != "presence" {
		return ""
	}
	return parts[2]
}
