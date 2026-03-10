//go:build !windows && cgo

package apiserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/observability/logging"
)

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

	// Read raw body — passed as-is to the route handler for fast extraction
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeErrorResponse(w, http.StatusBadRequest, "READ_ERROR", fmt.Sprintf("failed to read request body: %v", err))
		return
	}

	// Extract only metadata.headers (our HTTP API extension, not part of OpenAI format).
	// All other parsing (model, messages, content) is done by extractContentFast in the handler.
	headers := extractMetadataHeaders(body)

	// Delegate to the route handler (set from main.go, lives in extproc)
	resp, err := globalRouteHandler(body, headers)
	if err != nil {
		logging.Errorf("[RoutingAPI] Decision evaluation failed: %v", err)
		s.writeErrorResponse(w, http.StatusForbidden, "AUTHZ_DENIED", err.Error())
		return
	}

	s.writeJSONResponse(w, http.StatusOK, resp)
}

// extractMetadataHeaders extracts the optional metadata.headers from the request body.
// Returns an empty map if metadata is not present or parsing fails.
func extractMetadataHeaders(body []byte) map[string]string {
	headers := make(map[string]string)

	var meta struct {
		Metadata *struct {
			Headers map[string]string `json:"headers,omitempty"`
		} `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return headers
	}
	if meta.Metadata != nil {
		for k, v := range meta.Metadata.Headers {
			headers[strings.ToLower(k)] = v
		}
	}
	return headers
}
