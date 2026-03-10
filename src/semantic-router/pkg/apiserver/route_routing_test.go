//go:build !windows && cgo

package apiserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleRoute_RouterNotConfigured(t *testing.T) {
	// Ensure no global handler is set
	oldHandler := globalRouteHandler
	globalRouteHandler = nil
	defer func() { globalRouteHandler = oldHandler }()

	apiServer := &ClassificationAPIServer{}

	req := httptest.NewRequest("POST", "/v1/route", bytes.NewBufferString(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	apiServer.handleRoute(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	errorData, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected error object in response")
	}
	if errorData["code"] != "ROUTER_NOT_CONFIGURED" {
		t.Errorf("Expected error code ROUTER_NOT_CONFIGURED, got %v", errorData["code"])
	}
}

func TestHandleRoute_InvalidJSON(t *testing.T) {
	globalRouteHandler = func(body []byte, headers map[string]string) (map[string]interface{}, error) {
		return nil, &testRouteError{statusCode: http.StatusBadRequest, code: "EXTRACT_ERROR", message: "parse error"}
	}
	defer func() { globalRouteHandler = nil }()

	apiServer := &ClassificationAPIServer{}

	req := httptest.NewRequest("POST", "/v1/route", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	apiServer.handleRoute(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleRoute_EmptyBody(t *testing.T) {
	globalRouteHandler = func(body []byte, headers map[string]string) (map[string]interface{}, error) {
		return nil, &testRouteError{statusCode: http.StatusBadRequest, code: "EXTRACT_ERROR", message: "empty body"}
	}
	defer func() { globalRouteHandler = nil }()

	apiServer := &ClassificationAPIServer{}

	req := httptest.NewRequest("POST", "/v1/route", bytes.NewBufferString(""))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	apiServer.handleRoute(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleRoute_SuccessfulRouting(t *testing.T) {
	globalRouteHandler = func(body []byte, headers map[string]string) (map[string]interface{}, error) {
		return map[string]interface{}{
			"routing_decision": map[string]interface{}{
				"selected_model":      "Model-A",
				"decision_name":       "math",
				"decision_confidence": 0.95,
			},
			"matched_signals": map[string]interface{}{
				"domains": []string{"math"},
			},
			"request_id":         "",
			"processing_time_ms": int64(10),
		}, nil
	}
	defer func() { globalRouteHandler = nil }()

	apiServer := &ClassificationAPIServer{}

	body := `{"model":"auto","messages":[{"role":"user","content":"What is 2+2?"}]}`
	req := httptest.NewRequest("POST", "/v1/route", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	apiServer.handleRoute(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d. Body: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	rd, ok := resp["routing_decision"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected routing_decision in response")
	}
	if rd["selected_model"] != "Model-A" {
		t.Errorf("Expected selected_model Model-A, got %v", rd["selected_model"])
	}
	if rd["decision_name"] != "math" {
		t.Errorf("Expected decision_name math, got %v", rd["decision_name"])
	}
}

func TestHandleRoute_MetadataHeadersPassthrough(t *testing.T) {
	var capturedHeaders map[string]string
	globalRouteHandler = func(body []byte, headers map[string]string) (map[string]interface{}, error) {
		capturedHeaders = headers
		return map[string]interface{}{
			"routing_decision":  map[string]interface{}{},
			"matched_signals":   map[string]interface{}{},
			"request_id":        "test-req-001",
			"processing_time_ms": int64(5),
		}, nil
	}
	defer func() { globalRouteHandler = nil }()

	apiServer := &ClassificationAPIServer{}

	body := `{
		"model": "auto",
		"messages": [{"role": "user", "content": "hello"}],
		"metadata": {
			"headers": {
				"X-Request-Id": "test-req-001",
				"X-Authz-User-Id": "alice",
				"X-Authz-User-Groups": "premium,researchers"
			}
		}
	}`
	req := httptest.NewRequest("POST", "/v1/route", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	apiServer.handleRoute(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify headers were lowercased and passed through
	if capturedHeaders["x-request-id"] != "test-req-001" {
		t.Errorf("Expected x-request-id=test-req-001, got %v", capturedHeaders["x-request-id"])
	}
	if capturedHeaders["x-authz-user-id"] != "alice" {
		t.Errorf("Expected x-authz-user-id=alice, got %v", capturedHeaders["x-authz-user-id"])
	}
	if capturedHeaders["x-authz-user-groups"] != "premium,researchers" {
		t.Errorf("Expected x-authz-user-groups=premium,researchers, got %v", capturedHeaders["x-authz-user-groups"])
	}
}

func TestHandleRoute_NoMetadata(t *testing.T) {
	var capturedHeaders map[string]string
	globalRouteHandler = func(body []byte, headers map[string]string) (map[string]interface{}, error) {
		capturedHeaders = headers
		return map[string]interface{}{
			"routing_decision":  map[string]interface{}{},
			"matched_signals":   map[string]interface{}{},
			"processing_time_ms": int64(5),
		}, nil
	}
	defer func() { globalRouteHandler = nil }()

	apiServer := &ClassificationAPIServer{}

	body := `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/route", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	apiServer.handleRoute(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d", http.StatusOK, rr.Code)
	}

	if len(capturedHeaders) != 0 {
		t.Errorf("Expected empty headers when no metadata, got %v", capturedHeaders)
	}
}

func TestHandleRoute_ErrorStatusCodes(t *testing.T) {
	tests := []struct {
		name           string
		handler        RouteHandler
		expectedStatus int
		expectedCode   string
	}{
		{
			name: "Authz denied returns 403",
			handler: func(body []byte, headers map[string]string) (map[string]interface{}, error) {
				return nil, &testRouteError{statusCode: http.StatusForbidden, code: "AUTHZ_DENIED", message: "access denied"}
			},
			expectedStatus: http.StatusForbidden,
			expectedCode:   "AUTHZ_DENIED",
		},
		{
			name: "Missing field returns 400",
			handler: func(body []byte, headers map[string]string) (map[string]interface{}, error) {
				return nil, &testRouteError{statusCode: http.StatusBadRequest, code: "MISSING_FIELD", message: "model field is required"}
			},
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "MISSING_FIELD",
		},
		{
			name: "Extract error returns 400",
			handler: func(body []byte, headers map[string]string) (map[string]interface{}, error) {
				return nil, &testRouteError{statusCode: http.StatusBadRequest, code: "EXTRACT_ERROR", message: "invalid json"}
			},
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "EXTRACT_ERROR",
		},
		{
			name: "Generic error returns 500",
			handler: func(body []byte, headers map[string]string) (map[string]interface{}, error) {
				return nil, fmt.Errorf("unexpected internal error")
			},
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "INTERNAL_ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			globalRouteHandler = tt.handler
			defer func() { globalRouteHandler = nil }()

			apiServer := &ClassificationAPIServer{}

			body := `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`
			req := httptest.NewRequest("POST", "/v1/route", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			apiServer.handleRoute(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("Failed to unmarshal response: %v", err)
			}
			errorData, ok := resp["error"].(map[string]interface{})
			if !ok {
				t.Fatal("Expected error object in response")
			}
			if errorData["code"] != tt.expectedCode {
				t.Errorf("Expected error code %s, got %v", tt.expectedCode, errorData["code"])
			}
		})
	}
}

func TestExtractMetadataHeaders(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected map[string]string
	}{
		{
			name:     "No metadata",
			body:     `{"model":"auto","messages":[]}`,
			expected: map[string]string{},
		},
		{
			name:     "Empty metadata",
			body:     `{"model":"auto","metadata":{}}`,
			expected: map[string]string{},
		},
		{
			name:     "Metadata without headers",
			body:     `{"model":"auto","metadata":{"other":"value"}}`,
			expected: map[string]string{},
		},
		{
			name: "Headers present",
			body: `{"model":"auto","metadata":{"headers":{"X-Request-Id":"123","X-Authz-User-Id":"bob"}}}`,
			expected: map[string]string{
				"x-request-id":    "123",
				"x-authz-user-id": "bob",
			},
		},
		{
			name:     "Invalid JSON",
			body:     "not json",
			expected: map[string]string{},
		},
		{
			name: "Headers are lowercased",
			body: `{"metadata":{"headers":{"Content-Type":"application/json","CUSTOM-HEADER":"value"}}}`,
			expected: map[string]string{
				"content-type":  "application/json",
				"custom-header": "value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMetadataHeaders([]byte(tt.body))
			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d headers, got %d: %v", len(tt.expected), len(result), result)
			}
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("Expected header %s=%s, got %s", k, v, result[k])
				}
			}
		})
	}
}

// testRouteError implements the httpError interface for testing.
type testRouteError struct {
	statusCode int
	code       string
	message    string
}

func (e *testRouteError) Error() string    { return e.message }
func (e *testRouteError) HTTPStatus() int  { return e.statusCode }
func (e *testRouteError) ErrorCode() string { return e.code }

