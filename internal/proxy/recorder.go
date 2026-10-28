package proxy

import (
	"bytes"
	"mime"
	"net/http"
	"strings"

	"github.com/amangeldievkuu/llmproxy-go/internal/usage"
)

// maxRecordedBody caps how much of a JSON response is kept in memory for usage parsing.
const maxRecordedBody = 10 << 20

// recorder wraps an http.ResponseWriter and keeps what is needed to record the
// response: its status, its usage, and, if it may be cached, a copy of its
// body. Writes still reach the client immediately, so streaming works.
type recorder struct {
	http.ResponseWriter
	status         int
	cacheMax       int  // keep up to this many bytes of any body for the cache; 0 disables
	hideUsageChunk bool // the proxy added include_usage to the request, so the client did not ask for it

	started   bool
	stream    *usage.Stream // event streams are parsed as they pass, not buffered
	filter    *eventFilter  // drops the usage chunk when hideUsageChunk is set
	keep      int           // how many body bytes to keep
	body      bytes.Buffer
	truncated bool // the body grew past keep, so body holds only a prefix
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	if code >= 200 {
		r.start()
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.start()
	if r.stream != nil {
		r.stream.Write(b)
	}
	if r.filter != nil {
		return len(b), r.filter.Write(b)
	}
	return r.send(b)
}

// send keeps a copy of b if wanted and writes it to the client.
func (r *recorder) send(b []byte) (int, error) {
	if !r.truncated {
		if r.body.Len()+len(b) <= r.keep {
			r.body.Write(b)
		} else {
			r.body.Reset()
			r.truncated = true
		}
	}
	return r.ResponseWriter.Write(b)
}

// start decides, once the response headers are known, what to keep.
func (r *recorder) start() {
	if r.started {
		return
	}
	r.started = true
	r.keep = r.cacheMax
	mediaType, _, _ := mime.ParseMediaType(r.Header().Get("Content-Type"))
	switch {
	case mediaType == "text/event-stream":
		r.stream = new(usage.Stream)
		if r.hideUsageChunk {
			r.filter = &eventFilter{send: r.send}
		}
	case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		r.keep = max(r.keep, maxRecordedBody)
	}
}

// flush writes out anything the filter is still holding back. Call it once the
// upstream response has ended.
func (r *recorder) flush() {
	if r.filter != nil {
		r.filter.flush()
	}
}

// usage returns the model and token counts of the response.
func (r *recorder) usage() usage.Usage {
	if r.stream != nil {
		return r.stream.Usage()
	}
	if r.truncated {
		return usage.Usage{}
	}
	u, _ := usage.Parse(r.Header().Get("Content-Type"), r.body.Bytes())
	return u
}

// Unwrap lets http.ResponseController (used by ReverseProxy to flush streams)
// reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
