//go:build !windows && cgo

package extproc

import (
	"context"
	"fmt"
	"net/http"
	"time"

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

func (e *RouteError) Error() string      { return e.Message }
func (e *RouteError) HTTPStatus() int     { return e.StatusCode }
func (e *RouteError) ErrorCode() string   { return e.Code }

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

// emptyIfNil returns an empty slice instead of nil for clean JSON serialization.
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// HandleRouteRequest is the top-level entry point for the HTTP routing API.
// It extracts content, builds a RequestContext, performs routing, and returns
// the response as a map. Called from main.go via apiserver.SetRouteHandler.
func HandleRouteRequest(router *OpenAIRouter, body []byte, headers map[string]string) (map[string]interface{}, error) {
	start := time.Now()

	metrics.RecordRoutingAPIRequest()

	fast, err := extractContentFast(body)
	if err != nil {
		metrics.RecordRoutingAPIError("extract_error")
		return nil, &RouteError{
			StatusCode: http.StatusBadRequest,
			Code:       "EXTRACT_ERROR",
			Message:    fmt.Sprintf("failed to extract request fields: %v", err),
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
		TraceContext:     traceCtx,
		RequestModel:    fast.Model,
		UserContent:     fast.UserContent,
		RequestImageURL: fast.FirstImageURL,
	}
	if rid, ok := headers["x-request-id"]; ok {
		reqCtx.RequestID = rid
	}

	result, err := router.PerformRoutingDecision(fast.Model, fast.UserContent, fast.NonUserMessages, reqCtx)
	if err != nil {
		metrics.RecordRoutingAPIError("decision_error")
		// Authz errors from performDecisionEvaluation are 403
		return nil, &RouteError{
			StatusCode: http.StatusForbidden,
			Code:       "AUTHZ_DENIED",
			Message:    err.Error(),
		}
	}

	latencyMs := time.Since(start).Milliseconds()
	metrics.RecordRoutingAPILatency(float64(latencyMs) / 1000.0)

	logging.Infof("[RoutingAPI] request_id=%s model=%s decision=%s confidence=%.3f selected=%s method=%s latency=%dms",
		reqCtx.RequestID, fast.Model, result.DecisionName, result.Confidence,
		result.SelectedModel, reqCtx.VSRSelectionMethod, latencyMs)

	return BuildRouteResponse(result, reqCtx, latencyMs), nil
}

// BuildRouteResponse builds the full routing API JSON response from a RoutingResult
// and the RequestContext that was populated during decision evaluation.
// This is the single source of truth — any new signal added to RequestContext
// only needs to be added here.
func BuildRouteResponse(result *RoutingResult, ctx *RequestContext, processingTimeMs int64) map[string]interface{} {
	return map[string]interface{}{
		"routing_decision": map[string]interface{}{
			"selected_model":         result.SelectedModel,
			"decision_name":          result.DecisionName,
			"decision_confidence":    result.Confidence,
			"selected_category":      ctx.VSRSelectedCategory,
			"reasoning_mode":         ctx.VSRReasoningMode,
			"selected_modality":      ctx.VSRMatchedModality,
			"selection_method":       ctx.VSRSelectionMethod,
			"injected_system_prompt": ctx.VSRInjectedSystemPrompt,
		},
		"matched_signals": map[string]interface{}{
			"keywords":            emptyIfNil(ctx.VSRMatchedKeywords),
			"embeddings":          emptyIfNil(ctx.VSRMatchedEmbeddings),
			"domains":             emptyIfNil(ctx.VSRMatchedDomains),
			"fact_check":          emptyIfNil(ctx.VSRMatchedFactCheck),
			"user_feedback":       emptyIfNil(ctx.VSRMatchedUserFeedback),
			"preference":          emptyIfNil(ctx.VSRMatchedPreference),
			"language":            emptyIfNil(ctx.VSRMatchedLanguage),
			"context":             emptyIfNil(ctx.VSRMatchedContext),
			"context_token_count": ctx.VSRContextTokenCount,
			"complexity":          emptyIfNil(ctx.VSRMatchedComplexity),
			"modality":            emptyIfNil(ctx.VSRMatchedModality),
			"authz":               emptyIfNil(ctx.VSRMatchedAuthz),
			"jailbreak":           emptyIfNil(ctx.VSRMatchedJailbreak),
			"pii":                 emptyIfNil(ctx.VSRMatchedPII),
		},
		"security": map[string]interface{}{
			"jailbreak_detected": ctx.JailbreakDetected,
			"pii_detected":       ctx.PIIDetected,
			"pii_entities":       emptyIfNil(ctx.PIIEntities),
		},
		"request_id":         ctx.RequestID,
		"processing_time_ms": processingTimeMs,
	}
}
