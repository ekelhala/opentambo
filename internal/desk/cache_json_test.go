package desk

import (
	"encoding/json"
	"strings"
	"testing"
)

// The cache proxy rewrites manager-owned controls; every other byte of the
// client's JSON must survive verbatim. Integers beyond float64 precision are
// the canonical failure mode.
func lastRawForwarded(t *testing.T, fx *cacheFixture) string {
	t.Helper()
	fx.fake.mu.Lock()
	defer fx.fake.mu.Unlock()
	if len(fx.fake.rawBodies) == 0 {
		t.Fatal("backend captured no forwarded request")
	}
	return string(fx.fake.rawBodies[len(fx.fake.rawBodies)-1])
}

func assertRawField(t *testing.T, raw, field, want string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	value, ok := fields[field]
	if !ok {
		t.Fatalf("field %q dropped from the forwarded body: %s", field, raw)
	}
	if string(value) != want {
		t.Fatalf("field %q rewritten: got %s, want %s (body: %s)", field, value, want, raw)
	}
}

// Active caching must not round-trip the body through float64.
func TestCacheProxyPreservesLargeIntegers(t *testing.T) {
	fx := newCacheFixture(t)
	fx.post(t, chatBody(`[{"role":"user","content":"faithful"}]`, `"seed":9007199254740993,"negative":-9007199254740993`))
	raw := lastRawForwarded(t, fx)
	assertRawField(t, raw, "seed", "9007199254740993")
	assertRawField(t, raw, "negative", "-9007199254740993")
}

// An explicit cache_prompt:false bypass must not rewrite numeric values either.
func TestCacheProxyBypassPreservesLargeIntegers(t *testing.T) {
	fx := newCacheFixture(t)
	fx.post(t, chatBody(`[{"role":"user","content":"bypassed"}]`, `"cache_prompt":false,"seed":9007199254740993`))
	raw := lastRawForwarded(t, fx)
	assertRawField(t, raw, "seed", "9007199254740993")
	assertRawField(t, raw, "cache_prompt", "false")
}

// Requests the cache is not eligible for (ineligible shape) still keep their
// exact numeric payloads while the manager owns the slot.
func TestCacheProxyIneligibleRequestPreservesLargeIntegers(t *testing.T) {
	fx := newCacheFixture(t)
	fx.post(t, chatBody(`[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]`, `"seed":9007199254740993`))
	raw := lastRawForwarded(t, fx)
	assertRawField(t, raw, "seed", "9007199254740993")
}

// Nested and exotic values (large integers inside objects/arrays, fractions,
// exponents) must be forwarded unchanged.
func TestCacheProxyPreservesNestedRawValues(t *testing.T) {
	fx := newCacheFixture(t)
	fx.post(t, chatBody(`[{"role":"user","content":"nested"}]`, `"extension":{"big":9007199254740993,"list":[1.5,1e3,-9007199254740993]},"unicode":"ok é 😀"`))
	raw := lastRawForwarded(t, fx)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	extension, ok := fields["extension"]
	if !ok {
		t.Fatalf("unknown nested field dropped: %s", raw)
	}
	if !strings.Contains(string(extension), "9007199254740993") || strings.Contains(string(extension), "9007199254740992") {
		t.Fatalf("nested large integer rewritten: %s", extension)
	}
	if !strings.Contains(string(extension), "1e3") {
		t.Fatalf("exponent notation rewritten: %s", extension)
	}
	if value := string(fields["unicode"]); !strings.Contains(value, `\u00e9`) && !strings.Contains(value, "é") {
		t.Fatalf("unicode altered: %s", value)
	}
}
