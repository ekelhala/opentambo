package desk

// Request eligibility, server-derived token sequences, and prefix matching.
//
// Parity strategy: for chat requests the prompt comes from the child's own
// /apply-template endpoint fed with the incoming request fields, and tokens
// come from /tokenize with add_special=true and parse_special=true. This is
// the exact pipeline llama-server uses internally for /v1/chat/completions
// and /v1/completions (verified against the child build during the launch
// capability probe), so no template or tokenizer logic is reimplemented.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// The only cacheable endpoints in the initial scope.
const (
	endpointChatCompletions = "chat/completions"
	endpointCompletions     = "completions"
)

// cacheRequest is a parsed, eligibility-checked inference request. Requests
// that are not eligible keep the plain inference path; unsupported traffic
// still runs normally, it is only excluded from cache lookup/publication.
type cacheRequest struct {
	endpoint string
	// templateProjection holds the request fields forwarded to
	// /apply-template: everything except sampling/output controls.
	templateProjection json.RawMessage
	prompt             string // completions endpoint only
	tokens             []int32
}

// eligibleCacheEndpoints lists paths whose requests may participate.
func eligibleCacheEndpoint(endpoint string) bool {
	return endpoint == endpointChatCompletions || endpoint == endpointCompletions
}

// parseCacheRequest returns nil for requests that must not touch the cache.
func parseCacheRequest(endpoint string, body map[string]any) *cacheRequest {
	if !eligibleCacheEndpoint(endpoint) {
		return nil
	}
	if n, ok := body["n"]; ok {
		number, isNumber := n.(float64)
		if !isNumber || number != 1 {
			return nil // one completion per request only
		}
	}
	req := &cacheRequest{endpoint: endpoint}
	switch endpoint {
	case endpointChatCompletions:
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) == 0 {
			return nil
		}
		for _, item := range messages {
			message, ok := item.(map[string]any)
			if !ok {
				return nil
			}
			// String content only. Arrays (multimodal parts), images, and
			// other content shapes keep the existing path because their
			// normalization is not part of the verified token parity.
			switch message["content"].(type) {
			case string:
			case nil:
			default:
				return nil
			}
		}
		projection, err := applyTemplateProjection(body)
		if err != nil {
			return nil
		}
		req.templateProjection = projection
	case endpointCompletions:
		prompt, ok := body["prompt"].(string)
		if !ok || prompt == "" {
			return nil
		}
		req.prompt = prompt
	}
	return req
}

// deriveTokens asks the child server to render and tokenize the request the
// same way generation will.
func (r *cacheRequest) deriveTokens(ctx context.Context, client *llamaClient) error {
	prompt := r.prompt
	if r.endpoint == endpointChatCompletions {
		var err error
		prompt, err = client.applyTemplate(ctx, r.templateProjection)
		if err != nil {
			return err
		}
	}
	tokens, err := client.tokenize(ctx, prompt)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return errors.New("empty prompt tokens")
	}
	r.tokens = tokens
	return nil
}

// isPrefix reports whether prefix is a leading subsequence of tokens.
func isPrefix(prefix, tokens []int32) bool {
	if len(prefix) > len(tokens) {
		return false
	}
	for i := range prefix {
		if prefix[i] != tokens[i] {
			return false
		}
	}
	return true
}

// matchPrefix returns the candidate whose indexed prefix is the longest exact
// leading subsequence of the request tokens, or nil. Candidates must be
// ordered longest-first (see cacheStore.Candidates).
func matchPrefix(candidates []*CacheEntry, tokens []int32) *CacheEntry {
	for _, e := range candidates {
		if isPrefix(e.PromptTokens, tokens) {
			return e
		}
	}
	return nil
}

// withInternalControls rewrites the request body with manager-owned slot and
// prompt-cache controls while preserving all other fields, including unknown
// ones. An explicit client cache_prompt:false is honored (no lookup, no
// publication, slot ownership still applies).
func withInternalControls(body map[string]any, cacheActive bool) (json.RawMessage, error) {
	body["id_slot"] = chatSlot
	if value, present := body["cache_prompt"]; !present || value != false {
		if cacheActive {
			body["cache_prompt"] = true
		}
	}
	return json.Marshal(body)
}

// applyTemplateProjection limits the /apply-template body to request fields
// that can influence the served prompt. Sampling/output controls are
// dropped; anything else is forwarded because the child's parser sees it too.
func applyTemplateProjection(body map[string]any) (json.RawMessage, error) {
	projection := map[string]any{"add_generation_prompt": true}
	for key, value := range body {
		switch key {
		case "model", "stream", "stream_options", "max_tokens", "max_completion_tokens", "n", "temperature", "top_k", "top_p", "min_p", "seed", "stop", "frequency_penalty", "presence_penalty", "repeat_penalty", "dry_multiplier", "dry_base", "dry_allowed_length", "dry_penalty_last_n", "mirostat", "mirostat_tau", "mirostat_eta", "ignore_eos", "logprobs", "top_logprobs", "user", "timeout", "id_slot", "cache_prompt", "logit_bias", "min_tokens", "seed_from_header", "samplers", "dynatemp_range", "dynatemp_exponent", "typical_p", "xtc_probability", "xtc_threshold", "top_n_sigma", "min_keep", "n_probs", "post_sampling_probs", "backend_sampling", "timings_per_token", "speculative.types":
			continue
		default:
			projection[key] = value
		}
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return nil, fmt.Errorf("project template fields: %w", err)
	}
	return encoded, nil
}
