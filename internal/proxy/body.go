package proxy

import (
	"bytes"
	"io"
	"net/http"
)

// maxReadBody caps how much of a request body is read to inspect it. Larger
// bodies are forwarded untouched and bypass the cache.
const maxReadBody = 4 << 20

// readBody reads the body of r so it can be inspected, then restores r.Body so
// the request can still be sent upstream. It reports false, with r.Body still
// yielding the whole body, when the body is larger than limit.
func readBody(r *http.Request, limit int) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil {
		r.Body.Close()
		return nil, false, err
	}
	if len(body) > limit {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
		return nil, false, nil
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, true, nil
}
