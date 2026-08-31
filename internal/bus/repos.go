package bus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// reposPath is the machine-level map of project-root → bus_id.
func reposPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentbus", "repos.json")
}

// ResolveBusID finds the bus ID for a given working directory by walking up
// the directory tree and checking for a prefix match in ~/.agentbus/repos.json.
// Returns an error with a clear setup instruction if no match is found.
func ResolveBusID(cwd string) (string, error) {
	repos, err := readRepos()
	if err != nil {
		return "", fmt.Errorf("read %s: %w\nrun: agentbus-setup <bus_id>", reposPath(), err)
	}

	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd %s: %w", cwd, err)
	}

	// Longest-prefix match: find the most specific registered root.
	best := ""
	bestID := ""
	for root, id := range repos {
		if strings.HasPrefix(abs+"/", root+"/") && len(root) > len(best) {
			best = root
			bestID = id
		}
	}
	if bestID == "" {
		return "", fmt.Errorf("no agentbus registration found for %s\nrun: agentbus-setup <bus_id>", abs)
	}
	return bestID, nil
}

// RegisterRepo writes root → busID into ~/.agentbus/repos.json.
// Called by agentbus-setup.
func RegisterRepo(root, busID string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root %s: %w", root, err)
	}

	repos, _ := readRepos() // ignore error — file may not exist yet
	if repos == nil {
		repos = make(map[string]string)
	}
	repos[abs] = busID

	path := reposPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(repos, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readRepos() (map[string]string, error) {
	data, err := os.ReadFile(reposPath())
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
