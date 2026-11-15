package desk

// Typed, bounded client for the llama-server internal loopback API. These
// endpoints (props, slots, apply-template, tokenize, slot save/restore/erase)
// are manager-control calls: they must never be exposed through the public
// /v1 gateway. Responses are always size-limited and parsed defensively.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// chatSlot is the single slot owned by the prompt-cache coordinator.
	chatSlot = 0
	// Small control responses are tiny; the template response can be as large
	// as the request prompt, so it gets its own larger bound.
	controlResponseLimit  = 1 << 20
	templateResponseLimit = 48 << 20
)

type llamaClient struct {
	base string
	http *http.Client
}

func newLlamaClient(port int, client *http.Client) *llamaClient {
	return &llamaClient{base: fmt.Sprintf("http://127.0.0.1:%d", port), http: client}
}

func (c *llamaClient) doJSON(ctx context.Context, method, path string, body any, limit int64) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		detail := string(contents)
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return nil, fmt.Errorf("llama-server %s %s: HTTP %d: %s", method, path, res.StatusCode, detail)
	}
	return contents, nil
}

type slotInfo struct {
	ID           int  `json:"id"`
	NCtx         int  `json:"n_ctx"`
	IsProcessing bool `json:"is_processing"`
}

type serverProps struct {
	BuildInfo  string `json:"build_info"`
	TotalSlots int    `json:"total_slots"`
}

func (c *llamaClient) props(ctx context.Context) (serverProps, error) {
	var out serverProps
	raw, err := c.doJSON(ctx, "GET", "/props", nil, controlResponseLimit)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("props: %w", err)
	}
	return out, nil
}

func (c *llamaClient) slots(ctx context.Context) ([]slotInfo, error) {
	var out []slotInfo
	raw, err := c.doJSON(ctx, "GET", "/slots", nil, controlResponseLimit)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("slots: %w", err)
	}
	return out, nil
}

// applyTemplate renders the served prompt with the child's own chat template
// engine, so the prompt used for token matching is the generation prompt.
func (c *llamaClient) applyTemplate(ctx context.Context, body json.RawMessage) (string, error) {
	raw, err := c.doJSON(ctx, "POST", "/apply-template", json.RawMessage(body), templateResponseLimit)
	if err != nil {
		return "", err
	}
	var out struct {
		Prompt string `json:"prompt"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("apply-template: %w", err)
	}
	if out.Prompt == "" {
		return "", errors.New("apply-template returned an empty prompt")
	}
	return out.Prompt, nil
}

// tokenize mirrors the engine's own chat/completions tokenization:
// add_special=true, parse_special=true.
func (c *llamaClient) tokenize(ctx context.Context, prompt string) ([]int32, error) {
	raw, err := c.doJSON(ctx, "POST", "/tokenize", map[string]any{"content": prompt, "add_special": true, "parse_special": true}, templateResponseLimit)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tokens []int32 `json:"tokens"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("tokenize: %w", err)
	}
	return out.Tokens, nil
}

type slotSnapshot struct {
	NSaved    int `json:"n_saved"`
	NWritten  int `json:"n_written"`
	TimingsMs struct {
		SaveMs    float64 `json:"save_ms"`
		RestoreMs float64 `json:"restore_ms"`
	} `json:"timings"`
}

func (c *llamaClient) saveSlot(ctx context.Context, slot int, filename string) (slotSnapshot, error) {
	var out slotSnapshot
	raw, err := c.doJSON(ctx, "POST", fmt.Sprintf("/slots/%d?action=save", slot), map[string]string{"filename": filename}, controlResponseLimit)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("slot save: %w", err)
	}
	return out, nil
}

func (c *llamaClient) restoreSlot(ctx context.Context, slot int, filename string) (slotSnapshot, error) {
	var out slotSnapshot
	raw, err := c.doJSON(ctx, "POST", fmt.Sprintf("/slots/%d?action=restore", slot), map[string]string{"filename": filename}, controlResponseLimit)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("slot restore: %w", err)
	}
	return out, nil
}

func (c *llamaClient) eraseSlot(ctx context.Context, slot int) error {
	_, err := c.doJSON(ctx, "POST", fmt.Sprintf("/slots/%d?action=erase", slot), map[string]any{}, controlResponseLimit)
	return err
}

// waitSlotIdle polls until the owned slot stops processing. Used after client
// disconnects and cancellations so the next owner sees a settled slot.
// Confirmation requires the slot to appear in the listing as idle: a slot
// that is absent (or a control API that cannot be reached) is never treated
// as settled, because "unknown" must not grant the next owner access.
func (c *llamaClient) waitSlotIdle(ctx context.Context, slot int) bool {
	for {
		list, err := c.slots(ctx)
		if err == nil {
			for _, s := range list {
				if s.ID == slot && !s.IsProcessing {
					return true
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}
