package http

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// maxIDLen is the OpenAPI maxLength of the id path parameters.
const maxIDLen = 64

// pathParam returns a path segment, unescaped (an encoded '/' or space
// reaches the use case and is answered there). invalid is returned for a
// segment that is not valid percent-encoding.
func pathParam(r *http.Request, name string, invalid error) (string, error) {
	v, err := url.PathUnescape(chi.URLParam(r, name))
	if err != nil {
		return "", invalid
	}
	return v, nil
}

// idParam reads a path id, rejecting the shapes the OpenAPI parameter never
// allows: over-long, blank, or carrying white space or a slash. The resource
// prefix itself is NOT enforced here — an unknown id of the right shape is
// the use case's 404, not the adapter's 400.
func idParam(r *http.Request, name string, invalid error) (string, error) {
	id, err := pathParam(r, name, invalid)
	if err != nil {
		return "", err
	}
	if id == "" || len(id) > maxIDLen {
		return "", invalid
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c == ' ' || c == '\t' || c == '/' || c == 0 {
			return "", invalid
		}
	}
	return id, nil
}

// lineNoParam reads {lineNo}: an integer of at least 1.
func lineNoParam(r *http.Request) (int32, error) {
	raw := chi.URLParam(r, "lineNo")
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: lineNo must be an integer of at least 1", errMalformedRequest)
	}
	return int32(n), nil
}

// versionParam reads {version}: an integer of at least 1.
func versionParam(r *http.Request) (int64, error) {
	raw := chi.URLParam(r, "version")
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: version must be an integer of at least 1", errMalformedRequest)
	}
	return n, nil
}

// orderRefParam reads the required, non-blank orderRef query parameter of
// GET /returns.
func orderRefParam(r *http.Request) (string, error) {
	raw := r.URL.Query().Get("orderRef")
	if raw == "" {
		return "", fmt.Errorf("%w: orderRef is a required query parameter", errMalformedRequest)
	}
	if len(raw) > 64 {
		return "", fmt.Errorf("%w: orderRef must be at most 64 characters", errMalformedRequest)
	}
	return raw, nil
}
