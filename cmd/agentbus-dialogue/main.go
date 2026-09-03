// agentbus-dialogue is an interactive CLI tool for developers to interact
// with AI agents via agentbus topics (by default, listening on user.in and
// publishing replies to user.out).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
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

// setTerminalProgress sets the VS Code / ConEmu taskbar/tab progress state:
// state: 0 = none/clear, 1 = normal, 2 = error (red/paused), 3 = indeterminate (spinner), 4 = warning
func setTerminalProgress(state int, progress int) {
	if state == 0 {
		fmt.Print("\033]9;4;0;0\007")
	} else {
		fmt.Printf("\033]9;4;%d;%d\007", state, progress)
	}
	_ = os.Stdout.Sync()
}

// setTerminalTitle updates the window/tab title across xterm, VS Code terminal, and tmux.
func setTerminalTitle(title string) {
	seq := fmt.Sprintf("\033]0;%s\007\033]2;%s\007", title, title)

	if os.Getenv("TMUX") != "" {
		escaped := strings.ReplaceAll(seq, "\033", "\033\033")
		fmt.Printf("\033Ptmux;%s\033\\", escaped)
	} else {
		fmt.Print(seq)
	}
	_ = os.Stdout.Sync()
}

// renderSimpleMarkdown renders headers, bold, code blocks, lists, and inline code with ANSI formatting.
func renderSimpleMarkdown(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	inCodeBlock := false

	boldRegex := regexp.MustCompile(`\*\*(.*?)\*\*`)
	codeRegex := regexp.MustCompile("`([^`]+)`")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Fenced code block toggle
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				lang := strings.TrimPrefix(trimmed, "```")
				if lang != "" {
					out = append(out, fmt.Sprintf("  \033[90m┌─── [%s] ───\033[0m", lang))
				} else {
					out = append(out, "  \033[90m┌────────────────\033[0m")
				}
			} else {
				out = append(out, "  \033[90m└────────────────\033[0m")
			}
			continue
		}

		if inCodeBlock {
			out = append(out, fmt.Sprintf("  \033[90m│\033[0m \033[36m%s\033[0m", line))
			continue
		}

		// Headers
		if strings.HasPrefix(trimmed, "### ") {
			text := strings.TrimPrefix(trimmed, "### ")
			out = append(out, fmt.Sprintf("\033[1;34m### %s\033[0m", text))
			continue
		} else if strings.HasPrefix(trimmed, "## ") {
			text := strings.TrimPrefix(trimmed, "## ")
			out = append(out, fmt.Sprintf("\033[1;36m## %s\033[0m", text))
			continue
		} else if strings.HasPrefix(trimmed, "# ") {
			text := strings.TrimPrefix(trimmed, "# ")
			out = append(out, fmt.Sprintf("\033[1;35m# %s\033[0m", text))
			continue
		}

		// List items
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			item := strings.TrimPrefix(trimmed, "- ")
			item = strings.TrimPrefix(item, "* ")
			item = boldRegex.ReplaceAllString(item, "\033[1m$1\033[22m")
			item = codeRegex.ReplaceAllString(item, "\033[33m$1\033[39m")
			out = append(out, fmt.Sprintf("  \033[33m•\033[0m %s", item))
			continue
		}

		// Numbered list items
		if matched, _ := regexp.MatchString(`^\d+\.\s`, trimmed); matched {
			re := regexp.MustCompile(`^(\d+\.)\s*(.*)$`)
			parts := re.FindStringSubmatch(trimmed)
			if len(parts) == 3 {
				item := parts[2]
				item = boldRegex.ReplaceAllString(item, "\033[1m$1\033[22m")
				item = codeRegex.ReplaceAllString(item, "\033[33m$1\033[39m")
				out = append(out, fmt.Sprintf("  \033[33m%s\033[0m %s", parts[1], item))
				continue
			}
		}

		// Normal line with bold and inline code styling
		formatted := boldRegex.ReplaceAllString(line, "\033[1m$1\033[22m")
		formatted = codeRegex.ReplaceAllString(formatted, "\033[33m`$1`\033[39m")
		out = append(out, formatted)
	}

	return strings.Join(out, "\n")
}

// readUserReply reads user input using standard scanner.
// Single-line submissions: Enter immediately.
// Multi-line submissions: Trailing backslash '\' continues onto next line.
func readUserReply(scanner *bufio.Scanner) string {
	fmt.Print("\033[1;32mYour reply > \033[0m")
	var lines []string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasSuffix(line, "\\") {
			lines = append(lines, strings.TrimSuffix(line, "\\"))
			fmt.Print("\033[90m... > \033[0m")
			continue
		}

		lines = append(lines, line)
		break
	}

	reply := strings.TrimSpace(strings.Join(lines, "\n"))
	if reply == "" {
		reply = "(no response)"
	}
	return reply
}

func main() {
	inTopic := flag.String("in", "user.in", "topic to listen for incoming questions")
	outTopic := flag.String("out", "user.out", "topic to publish user replies")
	busIDFlag := flag.String("bus", "", "bus ID (defaults to auto-detect from cwd)")
	sessionID := flag.String("session", "dean", "user session identity")
	flag.Parse()

	busID := *busIDFlag
	if busID == "" {
		var err error
		busID, err = bus.ResolveBusID(resolveCWD())
		if err != nil {
			log.Fatalf("agentbus-dialogue: %v", err)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	nc, err := nats.Connect(natsURL())
	if err != nil {
		log.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("create JetStream context: %v", err)
	}

	if err := bus.EnsureStreams(ctx, js); err != nil {
		log.Fatalf("ensure streams: %v", err)
	}

	_ = bus.Subscribe(busID, *sessionID, *inTopic)

	idleTitle := fmt.Sprintf("agentbus-dialogue [%s]", busID)
	setTerminalTitle(idleTitle)
	setTerminalProgress(0, 0)

	fmt.Printf("\033[1;36m╔════════════════════════════════════════════════════════════╗\033[0m\n")
	fmt.Printf("\033[1;36m║               AGENTBUS INTERACTIVE DIALOGUE                ║\033[0m\n")
	fmt.Printf("\033[1;36m╚════════════════════════════════════════════════════════════╝\033[0m\n")
	fmt.Printf("  Bus:     \033[33m%s\033[0m\n", busID)
	fmt.Printf("  Listen:  \033[32m%s\033[0m\n", *inTopic)
	fmt.Printf("  Reply:   \033[32m%s\033[0m\n", *outTopic)
	fmt.Printf("  Session: \033[35m%s\033[0m\n", *sessionID)
	fmt.Printf("\n  \033[90mWaiting for incoming questions from AI agents...\033[0m\n\n")

	subj := bus.Subject(busID, *inTopic)
	cons, err := js.OrderedConsumer(ctx, bus.StreamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{subj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer on %s: %v", subj, err)
	}

	msgChan := make(chan schema.Message, 50)

	cc, err := cons.Consume(func(m jetstream.Msg) {
		var msg schema.Message
		if err := json.Unmarshal(m.Data(), &msg); err != nil {
			return
		}
		meta, err := m.Metadata()
		if err != nil {
			return
		}
		msg.Seq = meta.Sequence.Stream
		msgChan <- msg
	})
	if err != nil {
		log.Fatalf("start consume: %v", err)
	}
	defer cc.Stop()

	scanner := bufio.NewScanner(os.Stdin)

	for {
		select {
		case <-ctx.Done():
			setTerminalTitle(idleTitle)
			setTerminalProgress(0, 0)
			fmt.Println("\nExiting agentbus-dialogue.")
			return
		case msg := <-msgChan:
			// 1. Attention indicators:
			// - OSC 0/2 window title
			// - OSC 9;4 progress / warning state (3 = spinner/indeterminate)
			attentionTitle := fmt.Sprintf("💬 [ACTION REQUIRED] agentbus-dialogue [%s]", busID)
			setTerminalTitle(attentionTitle)
			setTerminalProgress(3, 100)

			// Terminal bell alert
			fmt.Print("\a")

			// Prominent Visual Attention Banner emphasizing sender
			fmt.Printf("\n\033[1;44;37m 🤖 FROM: %s \033[1;46;30m [%s] \033[0m \033[90m(seq=%d, %s)\033[0m\n",
				msg.From.Session, msg.From.Agent, msg.Seq, msg.TS)
			fmt.Printf("\033[36m%s\033[0m\n", strings.Repeat("━", 64))

			if len(msg.Refs) > 0 {
				fmt.Printf("  \033[90mRefs: %s\033[0m\n\n", strings.Join(msg.Refs, ", "))
			}

			// Render formatted Markdown body
			renderedBody := renderSimpleMarkdown(msg.Body)
			fmt.Printf("%s\n", renderedBody)
			fmt.Printf("\033[33m%s\033[0m\n\n", strings.Repeat("━", 64))

			replyText := readUserReply(scanner)

			replySeq := msg.Seq
			replyMsg := schema.Message{
				Topic:   *outTopic,
				From:    schema.From{Agent: "human", Session: *sessionID},
				TS:      time.Now().UTC().Format(time.RFC3339),
				Kind:    "answer",
				ReplyTo: &replySeq,
				Body:    replyText,
			}

			pubSeq, err := bus.Publish(ctx, js, busID, replyMsg)
			if err != nil {
				fmt.Printf("\033[31mError sending reply: %v\033[0m\n", err)
			} else {
				fmt.Printf("\n\033[1;32m✔ Reply posted to %s (seq=%d, reply_to=%d)\033[0m\n\n\n", *outTopic, pubSeq, msg.Seq)
			}

			// Reset terminal indicators back to idle
			setTerminalTitle(idleTitle)
			setTerminalProgress(0, 0)
		}
	}
}
