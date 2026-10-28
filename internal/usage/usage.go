// Package usage extracts the model name and token counts from LLM API responses.
//
// It understands OpenAI Chat Completions, OpenAI Responses and Anthropic Messages,
// both as plain JSON and as server-sent event (SSE) streams.
package usage

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Usage is the model and token counts of one response.
type Usage struct {
	Model        string
	InputTokens  int
	OutputTokens int
}

// Parse reads the usage from a response body. It reports false when the body
// names no model, for example an error response or a non-LLM endpoint.
func Parse(contentType string, body []byte) (Usage, bool) {
	var u Usage
	if strings.HasPrefix(contentType, "text/event-stream") {
		for line := range bytes.Lines(body) {
			data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
			if ok {
				u.add(bytes.TrimSpace(data))
			}
		}
	} else {
		u.add(body)
	}
	return u, u.Model != ""
}

// tokens covers both naming schemes: OpenAI Chat Completions uses
// prompt/completion, OpenAI Responses and Anthropic use input/output.
type tokens struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

type message struct {
	Model string  `json:"model"`
	Usage *tokens `json:"usage"`
}

// payload is one JSON body or one SSE event. Depending on the API, the model and
// usage sit at the top level, under "message" (Anthropic message_start) or under
// "response" (OpenAI Responses stream events).
type payload struct {
	message
	Message  *message `json:"message"`
	Response *message `json:"response"`
}

// add merges one payload into u. Streams report usage incrementally and
// cumulatively, so the largest count seen wins.
func (u *Usage) add(data []byte) {
	var p payload
	if json.Unmarshal(data, &p) != nil {
		return
	}
	for _, m := range []*message{&p.message, p.Message, p.Response} {
		if m == nil {
			continue
		}
		if u.Model == "" {
			u.Model = m.Model
		}
		if m.Usage != nil {
			u.InputTokens = max(u.InputTokens, m.Usage.PromptTokens, m.Usage.InputTokens)
			u.OutputTokens = max(u.OutputTokens, m.Usage.CompletionTokens, m.Usage.OutputTokens)
		}
	}
}
