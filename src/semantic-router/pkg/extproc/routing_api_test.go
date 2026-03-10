package extproc

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- ExtractContentFast (exported wrapper) ----------

func TestExtractContentFast_ValidRequest(t *testing.T) {
	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"What is 2+2?"}]}`)
	r, err := ExtractContentFast(body)
	require.NoError(t, err)
	assert.Equal(t, "auto", r.Model)
	assert.Equal(t, "What is 2+2?", r.UserContent)
}

func TestExtractContentFast_EmptyBody(t *testing.T) {
	_, err := ExtractContentFast([]byte(""))
	// extractContentFast should handle empty body gracefully
	// Model will be empty, which HandleRouteRequest validates
	assert.NoError(t, err)
}

// ---------- RouteError ----------

func TestRouteError_Interface(t *testing.T) {
	err := &RouteError{
		StatusCode: http.StatusBadRequest,
		Code:       "MISSING_FIELD",
		Message:    "model field is required",
	}

	assert.Equal(t, "model field is required", err.Error())
	assert.Equal(t, http.StatusBadRequest, err.HTTPStatus())
	assert.Equal(t, "MISSING_FIELD", err.ErrorCode())
}

func TestRouteError_Is403ForAuthz(t *testing.T) {
	err := &RouteError{
		StatusCode: http.StatusForbidden,
		Code:       "AUTHZ_DENIED",
		Message:    "user not authorized",
	}

	assert.Equal(t, http.StatusForbidden, err.HTTPStatus())
	assert.Equal(t, "AUTHZ_DENIED", err.ErrorCode())
}

// ---------- HandleRouteRequest ----------

func TestHandleRouteRequest_MissingModel(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	_, err := HandleRouteRequest(nil, body, nil)
	require.Error(t, err)

	re, ok := err.(*RouteError)
	require.True(t, ok, "Expected *RouteError, got %T", err)
	assert.Equal(t, http.StatusBadRequest, re.StatusCode)
	assert.Equal(t, "MISSING_FIELD", re.Code)
}

func TestHandleRouteRequest_InvalidJSON(t *testing.T) {
	body := []byte(`not json at all`)
	_, err := HandleRouteRequest(nil, body, nil)
	require.Error(t, err)

	re, ok := err.(*RouteError)
	require.True(t, ok, "Expected *RouteError, got %T", err)
	assert.Equal(t, http.StatusBadRequest, re.StatusCode)
	assert.Equal(t, "EXTRACT_ERROR", re.Code)
}

func TestHandleRouteRequest_EmptyModel(t *testing.T) {
	body := []byte(`{"model":"","messages":[{"role":"user","content":"hello"}]}`)
	_, err := HandleRouteRequest(nil, body, nil)
	require.Error(t, err)

	re, ok := err.(*RouteError)
	require.True(t, ok, "Expected *RouteError, got %T", err)
	assert.Equal(t, http.StatusBadRequest, re.StatusCode)
	assert.Equal(t, "MISSING_FIELD", re.Code)
}

// ---------- BuildRouteResponse ----------

func TestBuildRouteResponse_Structure(t *testing.T) {
	result := &RoutingResult{
		DecisionName:  "math",
		Confidence:    0.95,
		SelectedModel: "Model-A",
	}

	ctx := &RequestContext{
		RequestID:                     "req-123",
		VSRSelectedCategory:          "mathematics",
		VSRReasoningMode:             "on",
		VSRSelectionMethod:           "elo",
		VSRMatchedKeywords:           []string{"math_keywords"},
		VSRMatchedDomains:            []string{"mathematics"},
		VSRMatchedEmbeddings:         nil, // Should become []
		VSRMatchedFactCheck:          nil,
		VSRMatchedUserFeedback:       nil,
		VSRMatchedPreference:         nil,
		VSRMatchedLanguage:           []string{"en"},
		VSRMatchedContext:            nil,
		VSRContextTokenCount:         42,
		VSRMatchedComplexity:         nil,
		VSRMatchedModality:           []string{"AR"},
		VSRMatchedAuthz:              nil,
		VSRMatchedJailbreak:          nil,
		VSRMatchedPII:                nil,
		JailbreakDetected:            false,
		PIIDetected:                  false,
		PIIEntities:                  nil,
		VSRInjectedSystemPrompt:      true,
	}

	resp := BuildRouteResponse(result, ctx, 12)

	// Verify top-level structure
	assert.Equal(t, "req-123", resp["request_id"])
	assert.Equal(t, int64(12), resp["processing_time_ms"])

	// Verify routing_decision
	rd, ok := resp["routing_decision"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "Model-A", rd["selected_model"])
	assert.Equal(t, "math", rd["decision_name"])
	assert.Equal(t, 0.95, rd["decision_confidence"])
	assert.Equal(t, "mathematics", rd["selected_category"])
	assert.Equal(t, "on", rd["reasoning_mode"])
	assert.Equal(t, "elo", rd["selection_method"])
	assert.Equal(t, true, rd["injected_system_prompt"])

	// Verify matched_signals
	ms, ok := resp["matched_signals"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, []string{"math_keywords"}, ms["keywords"])
	assert.Equal(t, []string{"mathematics"}, ms["domains"])
	assert.Equal(t, []string{"en"}, ms["language"])
	assert.Equal(t, []string{"AR"}, ms["modality"])
	assert.Equal(t, 42, ms["context_token_count"])

	// Verify nil slices become empty slices (not null in JSON)
	assert.Equal(t, []string{}, ms["embeddings"])
	assert.Equal(t, []string{}, ms["fact_check"])
	assert.Equal(t, []string{}, ms["jailbreak"])
	assert.Equal(t, []string{}, ms["pii"])

	// Verify security
	sec, ok := resp["security"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, sec["jailbreak_detected"])
	assert.Equal(t, false, sec["pii_detected"])
	assert.Equal(t, []string{}, sec["pii_entities"])
}

func TestBuildRouteResponse_SecurityDetected(t *testing.T) {
	result := &RoutingResult{
		DecisionName:  "blocked",
		SelectedModel: "",
	}

	ctx := &RequestContext{
		JailbreakDetected: true,
		PIIDetected:       true,
		PIIEntities:       []string{"EMAIL", "PHONE_NUMBER"},
	}

	resp := BuildRouteResponse(result, ctx, 5)

	sec, ok := resp["security"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, sec["jailbreak_detected"])
	assert.Equal(t, true, sec["pii_detected"])
	assert.Equal(t, []string{"EMAIL", "PHONE_NUMBER"}, sec["pii_entities"])
}

// ---------- emptyIfNil ----------

func TestEmptyIfNil(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "nil becomes empty slice",
			input:    nil,
			expected: []string{},
		},
		{
			name:     "empty slice stays empty",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "non-empty slice unchanged",
			input:    []string{"a", "b"},
			expected: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := emptyIfNil(tt.input)
			require.NotNil(t, result, "Result should never be nil")
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ---------- JSON serialization ----------

func TestBuildRouteResponse_JSONSerializable(t *testing.T) {
	result := &RoutingResult{
		DecisionName:  "test",
		Confidence:    0.5,
		SelectedModel: "model-x",
	}
	ctx := &RequestContext{}

	resp := BuildRouteResponse(result, ctx, 1)

	// Verify it can be marshaled to JSON without error
	_, err := json.Marshal(resp)
	require.NoError(t, err, "Response should be JSON serializable")
}

func TestBuildRouteResponse_NilSlicesNotNullInJSON(t *testing.T) {
	result := &RoutingResult{}
	ctx := &RequestContext{} // All slice fields are nil

	resp := BuildRouteResponse(result, ctx, 0)

	data, err := json.Marshal(resp)
	require.NoError(t, err)

	// Verify nil slices are serialized as [] not null
	jsonStr := string(data)
	assert.NotContains(t, jsonStr, `"keywords":null`)
	assert.NotContains(t, jsonStr, `"domains":null`)
	assert.NotContains(t, jsonStr, `"jailbreak":null`)
	assert.NotContains(t, jsonStr, `"pii":null`)
	assert.NotContains(t, jsonStr, `"pii_entities":null`)
}
