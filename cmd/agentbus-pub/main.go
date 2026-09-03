// agentbus-pub is a minimal CLI for publishing a single message to an
// agentbus topic. Intended for manual testing and shell scripting.
//
// Usage:
//
//	agentbus-pub -topic user.in -body "hello" [-kind question] [-agent bob] [-session test]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/bus"
	"github.com/deanlorenz/agentbus/internal/schema"
)

func natsURL() string {
	if u := os.Getenv("AGENTBUS_NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

func resolveCWD() string {
	if d := os.Getenv("AGENTBUS_CWD"); d != "" {
		return d
	}
	cwd, _ := os.Getwd()
	return cwd
}

func main() {
	topic := flag.String("topic", "", "topic to publish to (required)")
	body := flag.String("body", "", "message body (required)")
	kind := flag.String("kind", "note", "message kind (note, question, answer, …)")
	agent := flag.String("agent", "human", "agent identifier")
	session := flag.String("session", "cli", "session identifier")
	busIDFlag := flag.String("bus", "", "bus ID (defaults to auto-detect from cwd)")
	flag.Parse()

	if *topic == "" || *body == "" {
		fmt.Fprintln(os.Stderr, "usage: agentbus-pub -topic <topic> -body <body> [-kind <kind>] [-agent <agent>] [-session <session>]")
		os.Exit(1)
	}

	busID := *busIDFlag
	if busID == "" {
		var err error
		busID, err = bus.ResolveBusID(resolveCWD())
		if err != nil {
			log.Fatalf("agentbus-pub: %v", err)
		}
	}

	nc, err := nats.Connect(natsURL(), nats.Timeout(3*time.Second))
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bus.EnsureStreams(ctx, js); err != nil {
		log.Fatalf("ensure streams: %v", err)
	}

	msg := schema.Message{
		Topic: *topic,
		From:  schema.From{Agent: *agent, Session: *session},
		TS:    time.Now().UTC().Format(time.RFC3339),
		Kind:  *kind,
		Body:  *body,
	}

	seq, err := bus.Publish(ctx, js, busID, msg)
	if err != nil {
		log.Fatalf("publish: %v", err)
	}
	fmt.Printf("published to %s (seq=%d)\n", *topic, seq)
}
