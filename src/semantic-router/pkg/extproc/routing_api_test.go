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

// ---------- HandleRouteRequest ----------

func TestHandleRouteRequest_NilRouter(t *testing.T) {
	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	_, err := HandleRouteRequest(nil, body, nil)
	require.Error(t, err)

	re, ok := err.(*RouteError)
	require.True(t, ok, "Expected *RouteError, got %T", err)
	assert.Equal(t, http.StatusServiceUnavailable, re.StatusCode)
	assert.Equal(t, "ROUTER_NOT_READY", re.Code)
}

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
	// Error message should NOT contain internal details
	assert.NotContains(t, re.Message, "json")
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

// ---------- buildRouteResponse ----------

func TestBuildRouteResponse_OpenAI_Minimal(t *testing.T) {
	router := &OpenAIRouter{}
	result := &RoutingResult{SelectedModel: "Model-A", DecisionName: "math", Confidence: 0.95}
	body := []byte(`{"model":"Model-A","messages":[{"role":"user","content":"hello"}]}`)

	resp, err := buildRouteResponse(router, result, body)
	require.NoError(t, err)

	assert.Equal(t, "Model-A", resp["model"])
	assert.Equal(t, false, resp["body_changed"])
	assert.Equal(t, "math", resp["decision_name"])
	assert.Equal(t, 0.95, resp["decision_confidence"])

	// OpenAI response should NOT have body, headers_to_set, headers_to_remove
	_, hasBody := resp["body"]
	assert.False(t, hasBody, "OpenAI response should not include body")
	_, hasHeaders := resp["headers_to_set"]
	assert.False(t, hasHeaders, "OpenAI response should not include headers_to_set")
}

func TestBuildRouteResponse_JSONSerializable(t *testing.T) {
	router := &OpenAIRouter{}
	result := &RoutingResult{SelectedModel: "Model-A", DecisionName: "test", Confidence: 0.5}
	body := []byte(`{"model":"Model-A","messages":[{"role":"user","content":"hello"}]}`)

	resp, err := buildRouteResponse(router, result, body)
	require.NoError(t, err)

	_, marshalErr := json.Marshal(resp)
	require.NoError(t, marshalErr)
}

// ---------- HandleTranslateResponse ----------

func TestHandleTranslateResponse_AnthropicToOpenAI(t *testing.T) {
	body := []byte(`{
		"api_format": "anthropic",
		"model": "Model-B",
		"response_body": {
			"id": "msg-mock-123",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Contract law governs agreements."}],
			"model": "Model-B",
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 8}
		}
	}`)

	resp, err := HandleTranslateResponse(body)
	require.NoError(t, err)

	assert.Equal(t, true, resp["translated"])

	translatedBody, ok := resp["translated_body"].(json.RawMessage)
	require.True(t, ok, "Expected json.RawMessage, got %T", resp["translated_body"])

	var openAIResp map[string]interface{}
	require.NoError(t, json.Unmarshal(translatedBody, &openAIResp))
	assert.Equal(t, "chat.completion", openAIResp["object"])
}

func TestHandleTranslateResponse_OpenAIPassthrough(t *testing.T) {
	body := []byte(`{
		"api_format": "openai",
		"model": "Model-A",
		"response_body": {"id": "cmpl-123", "object": "chat.completion"}
	}`)

	resp, err := HandleTranslateResponse(body)
	require.NoError(t, err)
	assert.Equal(t, false, resp["translated"])
}

func TestHandleTranslateResponse_EmptyFormat(t *testing.T) {
	body := []byte(`{"model": "Model-A", "response_body": {"id": "cmpl-123"}}`)

	resp, err := HandleTranslateResponse(body)
	require.NoError(t, err)
	assert.Equal(t, false, resp["translated"])
}

func TestHandleTranslateResponse_UnsupportedFormat(t *testing.T) {
	body := []byte(`{"api_format": "gemini", "model": "Model-X", "response_body": {}}`)

	_, err := HandleTranslateResponse(body)
	require.Error(t, err)

	re, ok := err.(*RouteError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, re.StatusCode)
	assert.Equal(t, "UNSUPPORTED_FORMAT", re.Code)
}

func TestHandleTranslateResponse_InvalidJSON(t *testing.T) {
	_, err := HandleTranslateResponse([]byte(`not json`))
	require.Error(t, err)

	re, ok := err.(*RouteError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, re.StatusCode)
	// Error message should NOT contain internal details
	assert.NotContains(t, re.Message, "json")
}
