//go:build !windows && cgo

package apiserver

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/observability/logging"
)

// httpError is an interface for errors that carry HTTP status information.
// This avoids importing extproc (which would create a circular import).
type httpError interface {
	error
	HTTPStatus() int
	ErrorCode() string
}

// RouteHandler is a function that takes a raw request body, optional headers,
// and returns the routing decision as a JSON-serializable map.
// This avoids a circular import between apiserver and extproc.
type RouteHandler func(body []byte, headers map[string]string) (map[string]interface{}, error)

// globalRouteHandler is set during initialization via SetRouteHandler.
var globalRouteHandler RouteHandler

// SetRouteHandler sets the function used to perform routing decisions.
func SetRouteHandler(handler RouteHandler) {
	globalRouteHandler = handler
}

// registerRoutingRoutes registers the HTTP routing API endpoint.
func registerRoutingRoutes(mux *http.ServeMux, s *ClassificationAPIServer) {
	mux.HandleFunc("POST /v1/route", s.handleRoute)
}

// handleRoute processes POST /v1/route requests.
// It reads the raw body once and passes it to the route handler.
// The handler (in extproc) does all parsing via extractContentFast.
// The only parsing done here is extracting the optional metadata.headers
// field, which is specific to the HTTP API (not part of OpenAI format).
func (s *ClassificationAPIServer) handleRoute(w http.ResponseWriter, r *http.Request) {
	if globalRouteHandler == nil {
		s.writeErrorResponse(w, http.StatusServiceUnavailable, "ROUTER_NOT_CONFIGURED", "routing API not enabled")
		return
	}

	// Limit request body size to prevent resource exhaustion
	limitBody(r)

	// Read raw body — passed as-is to the route handler for fast extraction
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeErrorResponse(w, http.StatusBadRequest, "READ_ERROR", fmt.Sprintf("failed to read request body: %v", err))
		return
	}

	// Extract only metadata.headers using gjson (our HTTP API extension, not part of OpenAI format).
	// All other parsing (model, messages, content) is done by extractContentFast in the handler.
	headers := extractMetadataHeaders(body)

	// Delegate to the route handler (set from main.go, lives in extproc)
	resp, err := globalRouteHandler(body, headers)
	if err != nil {
		logging.Errorf("[RoutingAPI] Request failed: %v", err)
		if re, ok := err.(httpError); ok {
			s.writeErrorResponse(w, re.HTTPStatus(), re.ErrorCode(), re.Error())
		} else {
			s.writeErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		}
		return
	}

	s.writeJSONResponse(w, http.StatusOK, resp)
}

// extractMetadataHeaders extracts the optional metadata.headers from the request body
// using gjson for efficient partial parsing without unmarshaling the full body.
func extractMetadataHeaders(body []byte) map[string]string {
	headers := make(map[string]string)
	result := gjson.GetBytes(body, "metadata.headers")
	if !result.Exists() {
		return headers
	}
	result.ForEach(func(key, value gjson.Result) bool {
		headers[strings.ToLower(key.String())] = value.String()
		return true
	})
	return headers
}
