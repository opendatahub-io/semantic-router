//go:build !windows && cgo

package extproc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/anthropic"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/config"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/observability/logging"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/observability/metrics"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/observability/tracing"
	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/utils/entropy"
)

// RoutingResult contains the result of a routing decision evaluation.
type RoutingResult struct {
	DecisionName      string
	Confidence        float64
	ReasoningDecision entropy.ReasoningDecision
	SelectedModel     string
}

// RouteError is a typed error that carries an HTTP status code for proper error responses.
type RouteError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *RouteError) Error() string     { return e.Message }
func (e *RouteError) HTTPStatus() int   { return e.StatusCode }
func (e *RouteError) ErrorCode() string { return e.Code }

// PerformRoutingDecision evaluates signals and decisions for an HTTP routing request.
// It wraps the internal performDecisionEvaluation method with a clean public API.
func (r *OpenAIRouter) PerformRoutingDecision(model, userContent string, nonUserMessages []string, ctx *RequestContext) (*RoutingResult, error) {
	decisionName, confidence, reasoningDecision, selectedModel, err := r.performDecisionEvaluation(model, userContent, nonUserMessages, ctx)
	if err != nil {
		return nil, err
	}
	return &RoutingResult{
		DecisionName:      decisionName,
		Confidence:        confidence,
		ReasoningDecision: reasoningDecision,
		SelectedModel:     selectedModel,
	}, nil
}

// ExtractContentFast is the exported version of extractContentFast for use by the HTTP routing API.
func ExtractContentFast(body []byte) (*FastExtractResult, error) {
	return extractContentFast(body)
}

// HandleRouteRequest is the top-level entry point for the HTTP routing API.
// It extracts content, builds a RequestContext, performs routing, and returns
// the response as a map including request mutations (body + headers) for BBR.
func HandleRouteRequest(router *OpenAIRouter, body []byte, headers map[string]string) (map[string]interface{}, error) {
	start := time.Now()

	metrics.RecordRoutingAPIRequest()

	if router == nil {
		metrics.RecordRoutingAPIError("router_nil")
		return nil, &RouteError{
			StatusCode: http.StatusServiceUnavailable,
			Code:       "ROUTER_NOT_READY",
			Message:    "router not initialized",
		}
	}

	fast, err := extractContentFast(body)
	if err != nil {
		metrics.RecordRoutingAPIError("extract_error")
		return nil, &RouteError{
			StatusCode: http.StatusBadRequest,
			Code:       "EXTRACT_ERROR",
			Message:    "failed to extract request fields",
		}
	}
	if fast.Model == "" {
		metrics.RecordRoutingAPIError("missing_model")
		return nil, &RouteError{
			StatusCode: http.StatusBadRequest,
			Code:       "MISSING_FIELD",
			Message:    "model field is required",
		}
	}

	// Propagate trace context from headers (if present)
	traceCtx := tracing.ExtractTraceContext(context.Background(), headers)

	reqCtx := &RequestContext{
		Headers:         headers,
		StartTime:       start,
		TraceContext:    traceCtx,
		RequestModel:   fast.Model,
		UserContent:    fast.UserContent,
		RequestImageURL: fast.FirstImageURL,
	}
	if rid, ok := headers["x-request-id"]; ok {
		reqCtx.RequestID = rid
	}

	result, err := router.PerformRoutingDecision(fast.Model, fast.UserContent, fast.NonUserMessages, reqCtx)
	if err != nil {
		logging.Errorf("[RoutingAPI] Decision evaluation failed: %v", err)
		metrics.RecordRoutingAPIError("decision_error")
		return nil, &RouteError{
			StatusCode: http.StatusForbidden,
			Code:       "AUTHZ_DENIED",
			Message:    "request denied by authorization policy",
		}
	}

	// Use the selected model, or fall back to the original model if not auto-selected
	selectedModel := result.SelectedModel
	if selectedModel == "" {
		selectedModel = fast.Model
	}
	result.SelectedModel = selectedModel

	// Build minimal response with only what BBR needs
	resp, err := buildRouteResponse(router, result, body)
	if err != nil {
		logging.Errorf("[RoutingAPI] Route response build failed: %v", err)
		metrics.RecordRoutingAPIError("mutation_error")
		return nil, &RouteError{
			StatusCode: http.StatusInternalServerError,
			Code:       "MUTATION_ERROR",
			Message:    "failed to prepare request mutations",
		}
	}

	latencyMs := time.Since(start).Milliseconds()
	metrics.RecordRoutingAPILatency(float64(latencyMs) / 1000.0)

	logging.Infof("[RoutingAPI] request_id=%s model=%s decision=%s confidence=%.3f selected=%s method=%s latency=%dms",
		reqCtx.RequestID, fast.Model, result.DecisionName, result.Confidence,
		result.SelectedModel, reqCtx.VSRSelectionMethod, latencyMs)

	return resp, nil
}

// buildRouteResponse builds the minimal response that the BBR plugin needs.
// Only returns what's actionable: model, whether body/headers changed, and the
// new body/headers if they did. The plugin uses body_changed to decide whether
// to open and replace the body, avoiding unnecessary work.
func buildRouteResponse(router *OpenAIRouter, result *RoutingResult, originalBody []byte) (map[string]interface{}, error) {
	selectedModel := result.SelectedModel
	apiFormat := config.APIFormatOpenAI
	if router.Config != nil {
		apiFormat = router.Config.GetModelAPIFormat(selectedModel)
	}

	// Minimal response — only what BBR needs to act on
	resp := map[string]interface{}{
		"model":               selectedModel,
		"body_changed":        false,
		"decision_name":       result.DecisionName,
		"decision_confidence": result.Confidence,
	}

	if apiFormat == config.APIFormatAnthropic {
		// Parse and transform body to Anthropic format
		openAIRequest, err := parseOpenAIRequest(originalBody)
		if err != nil {
			return nil, fmt.Errorf("failed to parse request for Anthropic transformation: %w", err)
		}
		openAIRequest.Model = selectedModel

		anthropicBody, err := anthropic.ToAnthropicRequestBody(openAIRequest)
		if err != nil {
			return nil, fmt.Errorf("failed to transform to Anthropic format: %w", err)
		}

		var bodyJSON json.RawMessage
		if err := json.Unmarshal(anthropicBody, &bodyJSON); err != nil {
			return nil, fmt.Errorf("failed to parse transformed body: %w", err)
		}

		// Build headers for Anthropic API
		headersToSet := map[string]string{}
		for _, h := range anthropic.BuildRequestHeaders("", len(anthropicBody)) {
			headersToSet[h.Key] = h.Value
		}

		resp["body_changed"] = true
		resp["body"] = bodyJSON
		resp["headers_to_set"] = headersToSet
		resp["headers_to_remove"] = anthropic.HeadersToRemove()
	}

	return resp, nil
}

// HandleTranslateResponse translates a model response from a provider-specific format
// (e.g., Anthropic) back to OpenAI format. Called by BBR after receiving the model response.
func HandleTranslateResponse(requestBody []byte) (map[string]interface{}, error) {
	var req struct {
		APIFormat    string          `json:"api_format"`
		Model        string          `json:"model"`
		ResponseBody json.RawMessage `json:"response_body"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		return nil, &RouteError{
			StatusCode: http.StatusBadRequest,
			Code:       "PARSE_ERROR",
			Message:    "failed to parse translate request",
		}
	}

	if req.APIFormat == "" || req.APIFormat == config.APIFormatOpenAI {
		// No translation needed for OpenAI format
		return map[string]interface{}{
			"translated":      false,
			"translated_body": req.ResponseBody,
		}, nil
	}

	if req.APIFormat == config.APIFormatAnthropic {
		translatedBody, err := anthropic.ToOpenAIResponseBody(req.ResponseBody, req.Model)
		if err != nil {
			logging.Errorf("[RoutingAPI] Anthropic response translation failed: %v", err)
			return nil, &RouteError{
				StatusCode: http.StatusBadRequest,
				Code:       "TRANSLATION_ERROR",
				Message:    "failed to translate response",
			}
		}

		var bodyJSON json.RawMessage
		if err := json.Unmarshal(translatedBody, &bodyJSON); err != nil {
			return nil, &RouteError{
				StatusCode: http.StatusInternalServerError,
				Code:       "MARSHAL_ERROR",
				Message:    "failed to marshal translated response",
			}
		}

		return map[string]interface{}{
			"translated":      true,
			"translated_body": bodyJSON,
		}, nil
	}

	return nil, &RouteError{
		StatusCode: http.StatusBadRequest,
		Code:       "UNSUPPORTED_FORMAT",
		Message:    fmt.Sprintf("unsupported api_format: %s", req.APIFormat),
	}
}
