package bus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentbusHome returns the root of all agentbus local state.
func agentbusHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentbus")
}

// subsPath returns the path to a session's subscription list file.
func subsPath(busID, sessionID string) string {
	return filepath.Join(agentbusHome(), "subs", busID, sessionID+".json")
}

// MarkerPath returns the path the relay writes when new messages arrive on topic.
// Topic dots are preserved since they're valid in file names.
func MarkerPath(busID, sessionID, topic string) string {
	safe := topicToFilename(topic)
	return filepath.Join(agentbusHome(), "markers", busID, sessionID, safe+".marker")
}

// CursorPath returns the path the hook writes after fetching messages for topic.
func CursorPath(busID, sessionID, topic string) string {
	safe := topicToFilename(topic)
	return filepath.Join(agentbusHome(), "cursors", busID, sessionID, safe+".cursor")
}

// topicToFilename replaces characters unsafe in filenames.
// Dots are fine on Linux/macOS; slashes are not valid in NATS topics anyway.
func topicToFilename(topic string) string {
	return strings.ReplaceAll(topic, "/", "_")
}

// Subscriptions is the set of topics a session is watching.
type Subscriptions struct {
	Topics []string `json:"topics"`
}

// Subscribe adds topic to the session's subscription file (idempotent).
func Subscribe(busID, sessionID, topic string) error {
	subs, _ := readSubs(busID, sessionID)
	for _, t := range subs.Topics {
		if t == topic {
			return nil // already subscribed
		}
	}
	subs.Topics = append(subs.Topics, topic)
	return writeSubs(busID, sessionID, subs)
}

// Unsubscribe removes topic from the session's subscription file.
func Unsubscribe(busID, sessionID, topic string) error {
	subs, err := readSubs(busID, sessionID)
	if err != nil {
		return nil // file absent = nothing to remove
	}
	filtered := subs.Topics[:0]
	for _, t := range subs.Topics {
		if t != topic {
			filtered = append(filtered, t)
		}
	}
	subs.Topics = filtered
	return writeSubs(busID, sessionID, subs)
}

// ReadSubs returns the subscription list for a session.
func ReadSubs(busID, sessionID string) ([]string, error) {
	subs, err := readSubs(busID, sessionID)
	if err != nil {
		return nil, err
	}
	return subs.Topics, nil
}

func readSubs(busID, sessionID string) (Subscriptions, error) {
	data, err := os.ReadFile(subsPath(busID, sessionID))
	if err != nil {
		return Subscriptions{}, err
	}
	var s Subscriptions
	if err := json.Unmarshal(data, &s); err != nil {
		return Subscriptions{}, err
	}
	return s, nil
}

func writeSubs(busID, sessionID string, subs Subscriptions) error {
	path := subsPath(busID, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.Marshal(subs)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
