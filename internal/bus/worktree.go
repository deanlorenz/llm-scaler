package bus

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// watchedWorktreesPath returns the path to the watched-worktrees list file,
// respecting the AGENTBUS_WORKTREES_LIST override used in tests.
func watchedWorktreesPath() string {
	if p := os.Getenv("AGENTBUS_WORKTREES_LIST"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentbus", "watched-worktrees.list")
}

// RegisterWorktree appends worktree to the watched-worktrees list if it isn't
// already present. Idempotent. Also writes the per-mission presence JSON file
// under <worktree>/.claude/agentbus/presence/<mission>.json so the relay can
// tell which missions each worktree cares about.
func RegisterWorktree(worktree, mission string) error {
	if err := appendWorktree(worktree); err != nil {
		return fmt.Errorf("register worktree %s: %w", worktree, err)
	}
	if err := writePresenceFile(worktree, mission); err != nil {
		return fmt.Errorf("write presence file for %s/%s: %w", worktree, mission, err)
	}
	return nil
}

// appendWorktree adds worktree to ~/.agentbus/watched-worktrees.list if absent.
func appendWorktree(worktree string) error {
	path := watchedWorktreesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Check if already present to keep the file tidy.
	if present, err := containsWorktree(path, worktree); err == nil && present {
		return nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, worktree)
	return err
}

func containsWorktree(path, worktree string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == worktree {
			return true, nil
		}
	}
	return false, sc.Err()
}

// writePresenceFile writes a sentinel file so the relay knows this worktree
// cares about mission. Content is minimal — just a newline — because the real
// presence data is in NATS; this is only a local signal for the relay's stat check.
func writePresenceFile(worktree, mission string) error {
	dir := filepath.Join(worktree, ".claude", "agentbus", "presence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, mission+".json")
	// Only create; don't overwrite — re-registering the same mission is a no-op.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil // already registered, fine
		}
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, `{"registered":true}`)
	return err
}
