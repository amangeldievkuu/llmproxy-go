package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// maxPendingEvent caps how much of one event eventFilter holds back. A longer
// event is passed through unfiltered.
const maxPendingEvent = 8 << 20

// addStreamUsage asks for token usage in a streamed Chat Completions request
// that does not say whether it wants it: OpenAI only reports usage in a stream
// when stream_options.include_usage is set. It reports whether body changed.
func addStreamUsage(path string, body []byte) ([]byte, bool) {
	if !strings.HasSuffix(path, "/chat/completions") {
		return nil, false
	}
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return nil, false
	}
	var stream bool
	if json.Unmarshal(req["stream"], &stream) != nil || !stream {
		return nil, false
	}
	opts := map[string]json.RawMessage{}
	if raw, ok := req["stream_options"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &opts) != nil {
			return nil, false
		}
	}
	if _, ok := opts["include_usage"]; ok {
		return nil, false // the app chose, either way
	}
	opts["include_usage"] = json.RawMessage("true")

	var err error
	if req["stream_options"], err = marshal(opts); err != nil {
		return nil, false
	}
	out, err := marshal(req)
	if err != nil {
		return nil, false
	}
	return out, true
}

// marshal is json.Marshal without escaping <, > and & in strings, so the
// prompt reaches the API as the app wrote it.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// eventFilter passes an event stream on to send, minus the usage-only chunk
// that include_usage adds at the end of a Chat Completions stream. It holds
// back each event until it has seen the whole of it.
type eventFilter struct {
	send    func([]byte) (int, error)
	pending []byte
}

func (f *eventFilter) Write(p []byte) error {
	f.pending = append(f.pending, p...)
	var out []byte
	rest := f.pending
	for {
		end := eventEnd(rest)
		if end < 0 {
			break
		}
		if !isUsageChunk(rest[:end]) {
			out = append(out, rest[:end]...)
		}
		rest = rest[end:]
	}
	if len(rest) > maxPendingEvent {
		out = append(out, rest...)
		rest = nil
	}
	f.pending = f.pending[:copy(f.pending, rest)]
	if len(out) == 0 {
		return nil
	}
	_, err := f.send(out)
	return err
}

// flush sends an unfinished last event.
func (f *eventFilter) flush() error {
	if len(f.pending) == 0 {
		return nil
	}
	_, err := f.send(f.pending)
	f.pending = f.pending[:0]
	return err
}

// eventEnd returns the length of the first complete event in b, including the
// blank line that ends it, or -1.
func eventEnd(b []byte) int {
	end := -1
	for _, sep := range [][]byte{[]byte("\n\n"), []byte("\n\r\n")} {
		if i := bytes.Index(b, sep); i >= 0 && (end < 0 || i+len(sep) < end) {
			end = i + len(sep)
		}
	}
	return end
}

// isUsageChunk reports whether event is a Chat Completions chunk with no
// choices and a usage object: the one include_usage adds.
func isUsageChunk(event []byte) bool {
	for line := range bytes.Lines(event) {
		data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !ok {
			continue
		}
		var chunk struct {
			Choices *[]json.RawMessage `json:"choices"`
			Usage   json.RawMessage    `json:"usage"`
		}
		if json.Unmarshal(bytes.TrimSpace(data), &chunk) == nil &&
			chunk.Choices != nil && len(*chunk.Choices) == 0 &&
			len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			return true
		}
	}
	return false
}
