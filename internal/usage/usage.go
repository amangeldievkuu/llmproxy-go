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
	Model string
	// InputTokens counts every input token, including cache reads and writes.
	InputTokens      int
	CacheReadTokens  int // input tokens read from the provider's prompt cache
	CacheWriteTokens int // input tokens written to the provider's prompt cache
	OutputTokens     int
}

// Parse reads the usage from a response body. It reports false when the body
// names no model, for example an error response or a non-LLM endpoint.
func Parse(contentType string, body []byte) (Usage, bool) {
	var s Stream
	if strings.HasPrefix(contentType, "text/event-stream") {
		s.Write(body)
	} else {
		s.add(body)
	}
	u := s.Usage()
	return u, u.Model != ""
}

// maxLine caps how much of a single SSE line is kept. Longer lines are skipped.
const maxLine = 8 << 20

// Stream reads usage from a server-sent event stream as it is written, so the
// stream does not have to be kept in memory. The zero value is ready to use.
type Stream struct {
	model         string
	input, output int
	openaiCached  int // part of input
	cacheRead     int // Anthropic: not part of input
	cacheWrite    int // Anthropic: not part of input

	line    []byte // the current, unfinished line
	skipped bool   // the current line grew past maxLine
}

// Write consumes part of an event stream. It never returns an error.
func (s *Stream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.buffer(p)
			break
		}
		s.buffer(p[:i])
		s.endLine()
		p = p[i+1:]
	}
	return n, nil
}

func (s *Stream) buffer(p []byte) {
	if s.skipped || len(s.line)+len(p) > maxLine {
		s.line, s.skipped = s.line[:0], true
		return
	}
	s.line = append(s.line, p...)
}

func (s *Stream) endLine() {
	if !s.skipped {
		if data, ok := bytes.CutPrefix(bytes.TrimSpace(s.line), []byte("data:")); ok {
			s.add(bytes.TrimSpace(data))
		}
	}
	s.line, s.skipped = s.line[:0], false
}

// Usage returns the usage seen so far, including a final line with no newline.
func (s *Stream) Usage() Usage {
	if len(s.line) > 0 {
		s.endLine()
	}
	return Usage{
		Model:            s.model,
		InputTokens:      s.input + s.cacheRead + s.cacheWrite,
		CacheReadTokens:  s.openaiCached + s.cacheRead,
		CacheWriteTokens: s.cacheWrite,
		OutputTokens:     s.output,
	}
}

type details struct {
	CachedTokens int `json:"cached_tokens"`
}

// tokens covers both naming schemes: OpenAI Chat Completions uses
// prompt/completion, OpenAI Responses and Anthropic use input/output.
// OpenAI counts cached tokens inside the input count; Anthropic counts
// cache reads and writes separately from it.
type tokens struct {
	PromptTokens             int      `json:"prompt_tokens"`
	CompletionTokens         int      `json:"completion_tokens"`
	InputTokens              int      `json:"input_tokens"`
	OutputTokens             int      `json:"output_tokens"`
	PromptTokensDetails      *details `json:"prompt_tokens_details"`
	InputTokensDetails       *details `json:"input_tokens_details"`
	CacheReadInputTokens     int      `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int      `json:"cache_creation_input_tokens"`
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

// add merges one payload into s. Streams report usage incrementally and
// cumulatively, so the largest count seen wins.
func (s *Stream) add(data []byte) {
	var p payload
	if json.Unmarshal(data, &p) != nil {
		return
	}
	for _, m := range []*message{&p.message, p.Message, p.Response} {
		if m == nil {
			continue
		}
		if s.model == "" {
			s.model = m.Model
		}
		if t := m.Usage; t != nil {
			s.input = max(s.input, t.PromptTokens, t.InputTokens)
			s.output = max(s.output, t.CompletionTokens, t.OutputTokens)
			for _, d := range []*details{t.PromptTokensDetails, t.InputTokensDetails} {
				if d != nil {
					s.openaiCached = max(s.openaiCached, d.CachedTokens)
				}
			}
			s.cacheRead = max(s.cacheRead, t.CacheReadInputTokens)
			s.cacheWrite = max(s.cacheWrite, t.CacheCreationInputTokens)
		}
	}
}
