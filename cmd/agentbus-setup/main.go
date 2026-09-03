// agentbus-setup registers the current directory as a project root in
// ~/.agentbus/repos.json with the given bus ID. Run once per project.
//
// Usage: agentbus-setup <bus_id>
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/deanlorenz/agentbus/internal/bus"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "--help" || os.Args[1] == "-h" {
		fmt.Fprintf(os.Stderr, "usage: agentbus-setup <bus_id>\n\nRegisters the current directory as a project root in ~/.agentbus/repos.json.\nRun once per project from the repo root.\n")
		if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
			os.Exit(0)
		}
		os.Exit(1)
	}
	busID := os.Args[1]

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("getwd: %v", err)
	}

	if err := bus.RegisterRepo(cwd, busID); err != nil {
		log.Fatalf("register repo: %v", err)
	}

	fmt.Printf("registered %s -> %s\n", cwd, busID)
}
