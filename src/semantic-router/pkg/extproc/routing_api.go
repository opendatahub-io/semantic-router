//go:build !windows && cgo

package extproc

import (
	"context"
	"fmt"
	"time"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/utils/entropy"
)

// RoutingResult contains the result of a routing decision evaluation.
type RoutingResult struct {
	DecisionName      string
	Confidence        float64
	ReasoningDecision entropy.ReasoningDecision
	SelectedModel     string
}

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

	fast, err := extractContentFast(body)
	if err != nil {
		return nil, err
	}
	if fast.Model == "" {
		return nil, fmt.Errorf("model field is required")
	}

	reqCtx := &RequestContext{
		Headers:         headers,
		StartTime:       start,
		TraceContext:     context.Background(),
		RequestModel:    fast.Model,
		UserContent:     fast.UserContent,
		RequestImageURL: fast.FirstImageURL,
	}
	if rid, ok := headers["x-request-id"]; ok {
		reqCtx.RequestID = rid
	}

	result, err := router.PerformRoutingDecision(fast.Model, fast.UserContent, fast.NonUserMessages, reqCtx)
	if err != nil {
		return nil, err
	}

	return BuildRouteResponse(result, reqCtx, time.Since(start).Milliseconds()), nil
}

// BuildRouteResponse builds the full routing API JSON response from a RoutingResult
// and the RequestContext that was populated during decision evaluation.
// This is the single source of truth — any new signal added to RequestContext
// only needs to be added here.
func BuildRouteResponse(result *RoutingResult, ctx *RequestContext, processingTimeMs int64) map[string]interface{} {
	return map[string]interface{}{
		"routing_decision": map[string]interface{}{
			"selected_model":        result.SelectedModel,
			"decision_name":         result.DecisionName,
			"decision_confidence":   result.Confidence,
			"selected_category":     ctx.VSRSelectedCategory,
			"reasoning_mode":        ctx.VSRReasoningMode,
			"selected_modality":     ctx.VSRMatchedModality,
			"selection_method":      ctx.VSRSelectionMethod,
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
		"request_id":        ctx.RequestID,
		"processing_time_ms": processingTimeMs,
	}
}
