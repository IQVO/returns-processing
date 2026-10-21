package http

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// rejectNUL is the fleet's NUL-byte guard (.claude/rules/rest-api.md):
// Postgres rejects U+0000 in text, so it never reaches a use case.
//
//   - a NUL byte in the request PATH answers 404 (the path names no
//     resource; it is a miss, not a client-shape error);
//   - a NUL byte in the request BODY answers 400 malformed-request-body.
//
// It wraps the router OUTSIDE the Idempotency-Key middleware, so a request
// refused here is never recorded: the store holds no answer for its key and
// a corrected retry runs for real.
func rejectNUL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.ContainsRune(r.URL.Path, 0) {
			writeProblem(w, r, notFoundProblem, "the request path contains a NUL byte (U+0000)")
			return
		}
		var body []byte
		if r.Body != nil { // an in-process request can carry a nil body
			var err error
			body, err = io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
			_ = r.Body.Close()
			if err != nil || len(body) > maxBodyBytes {
				writeProblem(w, r, malformedBody, "request body is not valid JSON for this operation")
				return
			}
			if bytes.IndexByte(body, 0) >= 0 {
				writeProblem(w, r, malformedBody, "the request body contains a NUL byte (U+0000), which Postgres rejects")
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
