package desk

import (
	"encoding/json"
	"testing"
)

func TestParseCacheRequestEligibility(t *testing.T) {
	chat := func(body string) map[string]any {
		var fields map[string]any
		if err := json.Unmarshal([]byte(body), &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}
	if req := parseCacheRequest("embeddings", chat(`{"prompt":"x"}`)); req != nil {
		t.Fatal("embeddings must not participate")
	}
	if req := parseCacheRequest("chat/completions", chat(`{"n":2,"messages":[{"role":"user","content":"hi"}]}`)); req != nil {
		t.Fatal("multi-completion requests must not participate")
	}
	if req := parseCacheRequest("chat/completions", chat(`{"messages":[]}`)); req != nil {
		t.Fatal("empty messages must not participate")
	}
	if req := parseCacheRequest("chat/completions", chat(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)); req != nil {
		t.Fatal("multimodal content must not participate")
	}
	if req := parseCacheRequest("completions", chat(`{"prompt":["a","b"]}`)); req != nil {
		t.Fatal("batched prompts must not participate")
	}
	if req := parseCacheRequest("completions", chat(`{"prompt":""}`)); req != nil {
		t.Fatal("empty prompt must not participate")
	}
	req := parseCacheRequest("chat/completions", chat(`{"n":1,"messages":[{"role":"user","content":"hi"},{"role":"assistant"},{"role":"user","content":"again"}]}`))
	if req == nil || req.endpoint != endpointChatCompletions || len(req.templateProjection) == 0 {
		t.Fatalf("plain chat rejected: %+v", req)
	}
	req = parseCacheRequest("completions", chat(`{"prompt":"hello"}`))
	if req == nil || req.prompt != "hello" {
		t.Fatalf("plain completion rejected: %+v", req)
	}
}

func TestApplyTemplateProjectionDropsOutputControls(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal([]byte(`{
		"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}],
		"temperature":0.2,"max_tokens":99,"seed":7,"stop":["x"],"stream":true,"logprobs":true,
		"id_slot":9,"cache_prompt":false,"response_format":{"type":"json_object"},"custom_field":{"a":1}
	}`), &body); err != nil {
		t.Fatal(err)
	}
	projection, err := applyTemplateProjection(body)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(projection, &fields); err != nil {
		t.Fatal(err)
	}
	for _, dropped := range []string{"temperature", "max_tokens", "seed", "stop", "stream", "logprobs", "id_slot", "cache_prompt", "model"} {
		if _, ok := fields[dropped]; ok {
			t.Errorf("%s should not reach /apply-template", dropped)
		}
	}
	for _, kept := range []string{"messages", "tools", "response_format", "custom_field"} {
		if _, ok := fields[kept]; !ok {
			t.Errorf("%s should be forwarded to /apply-template", kept)
		}
	}
	if fields["add_generation_prompt"] != true {
		t.Error("projection must request the generation prompt")
	}
}

func TestPrefixMatching(t *testing.T) {
	long := []*CacheEntry{
		{ID: "long", PromptTokens: []int32{1, 2, 3, 4, 5}},
		{ID: "short", PromptTokens: []int32{1, 2}},
		{ID: "other", PromptTokens: []int32{9, 9}},
	}
	if match := matchPrefix(long, []int32{1, 2, 3}); match == nil || match.ID != "short" {
		t.Fatalf("expected the longest compatible candidate, got %+v", match)
	}
	if match := matchPrefix(long, []int32{1, 2, 3, 4, 5, 6}); match == nil || match.ID != "long" {
		t.Fatalf("expected longest prefix, got %+v", match)
	}
	if match := matchPrefix(long, []int32{7, 8}); match != nil {
		t.Fatalf("unrelated prompt matched %+v", match)
	}
	if isPrefix([]int32{1, 2}, []int32{2, 1}) {
		t.Fatal("order matters for prefixes")
	}
}

func TestWithInternalControls(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal([]byte(`{"model":"m","messages":[],"custom":{"x":1}}`), &body); err != nil {
		t.Fatal(err)
	}
	out, err := withInternalControls(body, true)
	if err != nil {
		t.Fatal(err)
	}
	var forwarded map[string]any
	if err = json.Unmarshal(out, &forwarded); err != nil {
		t.Fatal(err)
	}
	if forwarded["id_slot"] != float64(0) || forwarded["cache_prompt"] != true {
		t.Fatalf("manager controls missing: %s", out)
	}
	if _, ok := forwarded["custom"]; !ok {
		t.Fatal("unknown fields must survive the rewrite")
	}
	body["cache_prompt"] = false
	out, err = withInternalControls(body, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(out, &forwarded); err != nil {
		t.Fatal(err)
	}
	if forwarded["cache_prompt"] != false {
		t.Fatal("an explicit cache_prompt:false must be honored")
	}
	if forwarded["id_slot"] != float64(0) {
		t.Fatal("slot ownership applies even without lookup")
	}
}
