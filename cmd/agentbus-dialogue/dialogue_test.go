package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/deanlorenz/agentbus/internal/bus"
	"github.com/deanlorenz/agentbus/internal/schema"
)

func TestInteractiveDialogueFlow(t *testing.T) {
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Skipf("NATS not running on default URL: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bus.EnsureStreams(ctx, js); err != nil {
		t.Fatalf("EnsureStreams: %v", err)
	}

	busID := "llmd-scaler"
	inTopic := "user.in"
	outTopic := "user.out"

	// 1. Setup dialogue consumer on user.in
	inSubj := bus.Subject(busID, inTopic)
	dialogueCons, err := js.OrderedConsumer(ctx, bus.StreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{inSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create dialogue consumer: %v", err)
	}

	dialogueMsgChan := make(chan schema.Message, 1)
	ccDialogue, err := dialogueCons.Consume(func(m jetstream.Msg) {
		var msg schema.Message
		_ = json.Unmarshal(m.Data(), &msg)
		meta, _ := m.Metadata()
		msg.Seq = meta.Sequence.Stream
		dialogueMsgChan <- msg
	})
	if err != nil {
		t.Fatalf("start dialogue consume: %v", err)
	}
	defer ccDialogue.Stop()

	// 2. Setup agent consumer waiting on user.out
	outSubj := bus.Subject(busID, outTopic)
	agentCons, err := js.OrderedConsumer(ctx, bus.StreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{outSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create agent consumer: %v", err)
	}

	agentReplyChan := make(chan schema.Message, 1)
	ccAgent, err := agentCons.Consume(func(m jetstream.Msg) {
		var msg schema.Message
		_ = json.Unmarshal(m.Data(), &msg)
		meta, _ := m.Metadata()
		msg.Seq = meta.Sequence.Stream
		agentReplyChan <- msg
	})
	if err != nil {
		t.Fatalf("start agent consume: %v", err)
	}
	defer ccAgent.Stop()

	// 3. Agent publishes question
	questionSeq, err := bus.Publish(ctx, js, busID, schema.Message{
		Topic:   inTopic,
		From:    schema.From{Agent: "bob", Session: "test-session"},
		TS:      time.Now().UTC().Format(time.RFC3339),
		Kind:    "question",
		Body:    "Should we proceed with migration?",
	})
	if err != nil {
		t.Fatalf("publish question: %v", err)
	}

	// 4. Dialogue receives question
	select {
	case q := <-dialogueMsgChan:
		if q.Seq != questionSeq {
			t.Fatalf("expected question seq %d, got %d", questionSeq, q.Seq)
		}
		if q.Body != "Should we proceed with migration?" {
			t.Fatalf("unexpected question body: %s", q.Body)
		}

		// Dialogue sends reply back
		replySeq := q.Seq
		_, err := bus.Publish(ctx, js, busID, schema.Message{
			Topic:   outTopic,
			From:    schema.From{Agent: "human", Session: "dean"},
			TS:      time.Now().UTC().Format(time.RFC3339),
			Kind:    "answer",
			ReplyTo: &replySeq,
			Body:    "Yes, proceed.",
		})
		if err != nil {
			t.Fatalf("publish reply: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dialogue to receive question")
	}

	// 5. Agent receives answer
	select {
	case ans := <-agentReplyChan:
		if ans.ReplyTo == nil || *ans.ReplyTo != questionSeq {
			t.Fatalf("expected reply_to %d, got %v", questionSeq, ans.ReplyTo)
		}
		if ans.Body != "Yes, proceed." {
			t.Fatalf("unexpected answer body: %s", ans.Body)
		}
		if ans.From.Agent != "human" {
			t.Fatalf("expected human agent, got %s", ans.From.Agent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for agent to receive answer")
	}
}
