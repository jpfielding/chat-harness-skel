// Command smoke runs a live smoke test against configured providers.
// NOT run in CI. Use before releases and after SDK/provider version bumps.
//
//	go run ./scripts/smoke --live --provider=all
//	go run ./scripts/smoke --live --provider=anthropic --model=anthropic:claude-haiku-4-5
//
// Exits non-zero on any per-provider failure, but runs all providers first
// so a single quota issue doesn't mask others.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jpfielding/chat-harness-skel/pkg/chat"
	"github.com/jpfielding/chat-harness-skel/pkg/providers/anthropic"
	"github.com/jpfielding/chat-harness-skel/pkg/providers/ollama"
	"github.com/jpfielding/chat-harness-skel/pkg/providers/openai"
)

type runResult struct {
	provider string
	ok       bool
	msg      string
}

func main() {
	var (
		live     = flag.Bool("live", false, "required; explicit opt-in to real API calls")
		provider = flag.String("provider", "all", "comma-separated: anthropic,openai,ollama or 'all'")
		anthModel = flag.String("anth-model", "anthropic:claude-haiku-4-5", "anthropic model ref")
		oaiModel  = flag.String("oai-model", "openai:gpt-5-mini", "openai model ref")
		ollModel  = flag.String("oll-model", "ollama:llama3.1:8b", "ollama model ref")
		ollHost   = flag.String("ollama-host", "", "override Ollama base URL (defaults to OLLAMA_HOST or http://localhost:11434)")
		timeout   = flag.Duration("timeout", 90*time.Second, "per-provider timeout")
	)
	flag.Parse()

	if !*live {
		fmt.Fprintln(os.Stderr, "refusing to run without --live (real API calls)")
		os.Exit(2)
	}

	selected := map[string]bool{}
	for _, s := range strings.Split(*provider, ",") {
		selected[strings.TrimSpace(s)] = true
	}
	if selected["all"] {
		selected = map[string]bool{"anthropic": true, "openai": true, "ollama": true}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout*time.Duration(len(selected)))
	defer cancel()

	var results []runResult

	if selected["anthropic"] {
		key, err := anthropic.ResolveAPIKey()
		if err != nil {
			results = append(results, runResult{provider: "anthropic", ok: false, msg: err.Error()})
		} else {
			p, err := anthropic.New(anthropic.Config{APIKey: key})
			if err != nil {
				results = append(results, runResult{provider: "anthropic", ok: false, msg: err.Error()})
			} else {
				results = append(results, runProvider(ctx, "anthropic", *anthModel, p))
			}
		}
	}

	if selected["openai"] {
		key, err := openai.ResolveAPIKey()
		if err != nil {
			results = append(results, runResult{provider: "openai", ok: false, msg: err.Error()})
		} else {
			p, err := openai.New(openai.Config{APIKey: key})
			if err != nil {
				results = append(results, runResult{provider: "openai", ok: false, msg: err.Error()})
			} else {
				results = append(results, runProvider(ctx, "openai", *oaiModel, p))
			}
		}
	}

	if selected["ollama"] {
		base := *ollHost
		if base == "" {
			base = os.Getenv("OLLAMA_HOST")
		}
		p, err := ollama.New(ollama.Config{BaseURL: base})
		if err != nil {
			results = append(results, runResult{provider: "ollama", ok: false, msg: err.Error()})
		} else {
			results = append(results, runProvider(ctx, "ollama", *ollModel, p))
		}
	}

	fmt.Println("\n=== smoke summary ===")
	fails := 0
	for _, r := range results {
		status := "OK"
		if !r.ok {
			status = "FAIL"
			fails++
		}
		fmt.Printf("  %-10s %s  %s\n", r.provider, status, r.msg)
	}
	if fails > 0 {
		os.Exit(1)
	}
}

// runProvider exercises both Send (non-streaming) and Stream for one model.
func runProvider(ctx context.Context, name, model string, p chat.Provider) runResult {
	// Non-streaming.
	resp, err := p.Send(ctx, chat.Request{
		Model: model,
		Messages: []chat.Message{
			chat.UserText("Say hi in four words or fewer."),
		},
		Params: chat.GenerationParams{MaxTokens: 20},
	})
	if err != nil {
		return runResult{provider: name, ok: false, msg: "Send: " + err.Error()}
	}
	sendText := extractText(resp.Message)
	if strings.TrimSpace(sendText) == "" {
		return runResult{provider: name, ok: false, msg: "Send returned empty text"}
	}

	// Streaming.
	reader, err := p.Stream(ctx, chat.Request{
		Model: model,
		Messages: []chat.Message{
			chat.UserText("Count: one, two, three."),
		},
		Params: chat.GenerationParams{MaxTokens: 40},
	})
	if err != nil {
		return runResult{provider: name, ok: false, msg: "Stream: " + err.Error()}
	}
	defer reader.Close()

	var streamText string
	var sawStart, sawStop bool
	for {
		ev, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return runResult{provider: name, ok: false, msg: "Stream.Next: " + err.Error()}
		}
		switch ev.Kind {
		case chat.EvMessageStart:
			sawStart = true
		case chat.EvBlockDelta:
			streamText += ev.TextDelta
		case chat.EvMessageStop:
			sawStop = true
		}
	}
	if !sawStart || !sawStop {
		return runResult{provider: name, ok: false, msg: fmt.Sprintf("missing lifecycle events: start=%v stop=%v", sawStart, sawStop)}
	}
	if strings.TrimSpace(streamText) == "" {
		return runResult{provider: name, ok: false, msg: "Stream returned empty text"}
	}
	return runResult{provider: name, ok: true, msg: fmt.Sprintf("Send=%dt, Stream=%db", resp.Usage.OutputTokens, len(streamText))}
}

func extractText(m chat.Message) string {
	var out strings.Builder
	for _, b := range m.Content {
		if b.Kind == chat.BlockText {
			out.WriteString(b.Text)
		}
	}
	return out.String()
}
