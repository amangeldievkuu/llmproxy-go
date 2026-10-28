package proxy

import (
	"bytes"
	"net/http"
)

// maxRecordedBody caps how much of a response is kept in memory for usage parsing.
const maxRecordedBody = 10 << 20

// recorder wraps an http.ResponseWriter and keeps a copy of the status and body
// sent to the client. Writes still reach the client immediately, so streaming works.
type recorder struct {
	http.ResponseWriter
	status    int
	body      bytes.Buffer
	truncated bool
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.body.Len()+len(b) <= maxRecordedBody {
		r.body.Write(b)
	} else {
		r.truncated = true
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController (used by ReverseProxy to flush streams)
// reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
