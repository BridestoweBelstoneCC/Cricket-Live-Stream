// Package ai is every call the app makes to Claude: over commentary, match reports,
// captions, the camera spotter and the pre-match key check. All of it is optional —
// each caller has a non-AI fallback or is opt-in — and all of it uses one small, cheap
// model, so a club's AI bill stays in pence.
package ai

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Model is the one model every feature uses (the original's choice: short outputs,
// cheap per call).
const Model = "claude-haiku-4-5"

// ErrNoKey means no Anthropic API key is configured.
var ErrNoKey = errors.New("no Anthropic API key configured")

func client(key string, timeout time.Duration) anthropic.Client {
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(timeout))
	}
	return anthropic.NewClient(opts...)
}

func textOf(m *anthropic.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// Request is one prompt.
type Request struct {
	Key       string
	System    string
	Prompt    string
	MaxTokens int64
	// ImageJPEG, when set, is sent before the prompt (the camera spotter).
	ImageJPEG []byte
	Timeout   time.Duration
}

// Ask sends one prompt and returns the reply's text. Swappable for tests.
var Ask = func(ctx context.Context, r Request) (string, error) {
	if strings.TrimSpace(r.Key) == "" {
		return "", ErrNoKey
	}
	var blocks []anthropic.ContentBlockParamUnion
	if len(r.ImageJPEG) > 0 {
		blocks = append(blocks, anthropic.NewImageBlockBase64("image/jpeg", base64.StdEncoding.EncodeToString(r.ImageJPEG)))
	}
	blocks = append(blocks, anthropic.NewTextBlock(r.Prompt))
	params := anthropic.MessageNewParams{
		Model:     Model,
		MaxTokens: r.MaxTokens,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
	}
	if r.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: r.System}}
	}
	c := client(r.Key, r.Timeout)
	m, err := c.Messages.New(ctx, params)
	if err != nil {
		return "", err
	}
	if m.StopReason == anthropic.StopReasonRefusal {
		return "", errors.New("the model declined to answer")
	}
	return textOf(m), nil
}

// CheckKey proves a key works without spending tokens (listing models is free).
// Swappable for tests.
var CheckKey = func(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrNoKey
	}
	c := client(key, 15*time.Second)
	_, err := c.Models.List(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1)})
	return err
}

// StatusCode is the HTTP status of an API error (0 if it wasn't one).
func StatusCode(err error) int {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		return apierr.StatusCode
	}
	return 0
}
