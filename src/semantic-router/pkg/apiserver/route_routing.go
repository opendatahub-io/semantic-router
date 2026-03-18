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
type RouteHandler func(body []byte, headers map[string]string) (map[string]interface{}, error)

// TranslateResponseHandler is a function that takes a raw request body
// and returns the translated response as a JSON-serializable map.
type TranslateResponseHandler func(body []byte) (map[string]interface{}, error)

// globalRouteHandler is set during initialization via SetRouteHandler.
var globalRouteHandler RouteHandler

// globalTranslateResponseHandler is set during initialization via SetTranslateResponseHandler.
var globalTranslateResponseHandler TranslateResponseHandler

// SetRouteHandler sets the function used to perform routing decisions.
func SetRouteHandler(handler RouteHandler) {
	globalRouteHandler = handler
}

// SetTranslateResponseHandler sets the function used to translate model responses.
func SetTranslateResponseHandler(handler TranslateResponseHandler) {
	globalTranslateResponseHandler = handler
}

// registerRoutingRoutes registers the HTTP routing API endpoints.
func registerRoutingRoutes(mux *http.ServeMux, s *ClassificationAPIServer) {
	mux.HandleFunc("POST /v1/route", s.handleRoute)
	mux.HandleFunc("POST /v1/route/translate-response", s.handleTranslateResponse)
}

// allowedMetadataHeaders defines the headers that can be passed via metadata.headers.
// Only observability and identity headers are allowed — not auth credentials or
// privileged routing headers which must come from the actual HTTP request.
var allowedMetadataHeaders = map[string]bool{
	"x-request-id":        true,
	"x-authz-user-id":     true,
	"x-authz-user-groups": true,
	"traceparent":          true,
	"tracestate":           true,
}

// handleRoute processes POST /v1/route requests.
func (s *ClassificationAPIServer) handleRoute(w http.ResponseWriter, r *http.Request) {
	if globalRouteHandler == nil {
		s.writeErrorResponse(w, http.StatusServiceUnavailable, "ROUTER_NOT_CONFIGURED", "routing API not enabled")
		return
	}

	limitBody(r)

	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeErrorResponse(w, http.StatusBadRequest, "READ_ERROR", fmt.Sprintf("failed to read request body: %v", err))
		return
	}

	headers := extractMetadataHeaders(body)

	resp, err := globalRouteHandler(body, headers)
	if err != nil {
		logging.Errorf("[RoutingAPI] Request failed: %v", err)
		if re, ok := err.(httpError); ok {
			s.writeErrorResponse(w, re.HTTPStatus(), re.ErrorCode(), re.Error())
		} else {
			s.writeErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
		}
		return
	}

	s.writeJSONResponse(w, http.StatusOK, resp)
}

// handleTranslateResponse processes POST /v1/route/translate-response requests.
func (s *ClassificationAPIServer) handleTranslateResponse(w http.ResponseWriter, r *http.Request) {
	if globalTranslateResponseHandler == nil {
		s.writeErrorResponse(w, http.StatusServiceUnavailable, "ROUTER_NOT_CONFIGURED", "translate response API not enabled")
		return
	}

	limitBody(r)

	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeErrorResponse(w, http.StatusBadRequest, "READ_ERROR", fmt.Sprintf("failed to read request body: %v", err))
		return
	}

	resp, err := globalTranslateResponseHandler(body)
	if err != nil {
		logging.Errorf("[RoutingAPI] Translate response failed: %v", err)
		if re, ok := err.(httpError); ok {
			s.writeErrorResponse(w, re.HTTPStatus(), re.ErrorCode(), re.Error())
		} else {
			s.writeErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
		}
		return
	}

	s.writeJSONResponse(w, http.StatusOK, resp)
}

// extractMetadataHeaders extracts allowed headers from the optional metadata.headers
// field using gjson. Only headers in the allowlist are accepted to prevent privilege
// escalation via injected auth or routing headers (CWE-345).
func extractMetadataHeaders(body []byte) map[string]string {
	headers := make(map[string]string)
	result := gjson.GetBytes(body, "metadata.headers")
	if !result.Exists() {
		return headers
	}
	result.ForEach(func(key, value gjson.Result) bool {
		k := strings.ToLower(key.String())
		if allowedMetadataHeaders[k] {
			headers[k] = value.String()
		}
		return true
	})
	return headers
}
