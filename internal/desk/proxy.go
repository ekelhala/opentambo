package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
)

func proxyError(w http.ResponseWriter, status int, kind, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"message": message, "type": kind, "code": kind}})
}

// Listing managed processes avoids a health-check fanout on every model lookup.
// A loading child is listed too; its own HTTP 503 propagates to the caller.
func (a *App) proxyModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	models := []model{}
	a.mu.Lock()
	for _, run := range a.runs {
		if run.Status == "running" {
			models = append(models, model{run.Launch.Name, "model", run.Started.Unix(), "llama-desk"})
		}
	}
	a.mu.Unlock()
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	respond(w, 200, map[string]any{"object": "list", "data": models})
}

func (a *App) proxyInference(w http.ResponseWriter, r *http.Request) {
	// Large enough for long prompts and small inline images, but never unbounded.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		proxyError(w, 413, "invalid_request_error", "request exceeds the 32 MiB limit")
		return
	}
	var input struct {
		Model string `json:"model"`
	}
	if err = json.Unmarshal(body, &input); err != nil || input.Model == "" {
		proxyError(w, 400, "invalid_request_error", "provide a JSON object with a nonempty model name")
		return
	}
	a.mu.Lock()
	port := 0
	var runContext context.Context
	var coordinator *RunCache
	for _, run := range a.runs {
		if run.Status == "running" && run.Launch.Name == input.Model {
			port = run.Launch.Port
			runContext = run.ctx
			coordinator = run.cache
			break
		}
	}
	a.mu.Unlock()
	if port == 0 {
		proxyError(w, 404, "model_not_found", "model is not running; start it in llama-desk first")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if runContext != nil {
		stop := context.AfterFunc(runContext, cancel)
		defer stop()
	}
	var plan *cachePlan
	if coordinator != nil {
		// Cache-enabled runs serialize all slot-mutating traffic, including
		// requests that do not participate in cache lookup themselves.
		if err = coordinator.acquire(ctx); err != nil {
			proxyError(w, 503, "server_busy", err.Error())
			return
		}
		defer coordinator.release()
		var fields map[string]any
		if err = json.Unmarshal(body, &fields); err != nil {
			proxyError(w, 400, "invalid_request_error", "provide a JSON object")
			return
		}
		plan, body, err = coordinator.Prepare(ctx, strings.TrimPrefix(r.URL.Path, "/v1/"), fields)
		if err != nil {
			// A safe slot state could not be established; nothing was forwarded.
			proxyError(w, 502, "upstream_unavailable", err.Error())
			return
		}
	}
	r = r.WithContext(ctx)
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	observer := &observedWriter{ResponseWriter: w, status: 200}
	proxy := &httputil.ReverseProxy{
		Transport:     a.proxyTransport,
		FlushInterval: -1, // Flush each write, including server-sent events.
		Rewrite: func(out *httputil.ProxyRequest) {
			out.SetURL(target)
			// Gateway credentials terminate at LiteLLM. Never pass them to children.
			out.Out.Header.Del("Authorization")
			out.Out.Header.Del("X-Api-Key")
			out.Out.Header.Del("X-Litellm-Api-Key")
			out.Out.Header.Del("Cookie")
		},
		ModifyResponse: func(res *http.Response) error {
			res.Body = &trackingBody{ReadCloser: res.Body, observer: observer}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			observer.error = err
			proxyError(w, 502, "upstream_unavailable", "the selected llama-server could not be reached")
		},
	}
	proxy.ServeHTTP(observer, r)
	if coordinator != nil {
		responseOK := observer.status >= 200 && observer.status < 300 && observer.error == nil
		complete := ctx.Err() == nil && observer.streamError == nil && observer.sawEOF && observer.terminalStream()
		coordinator.Finish(ctx, plan, responseOK, complete)
	}
}

// observedWriter records enough about the forwarded response for snapshot
// decisions (status, copy errors, whether the stream terminated cleanly)
// without buffering it: only the last bytes are remembered for the SSE
// terminal check.
type observedWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	error       error
	streamError error
	sawEOF      bool
	tail        []byte
	eventStream bool
}

const observedTail = 64

func (o *observedWriter) WriteHeader(status int) {
	if !o.wroteHeader {
		o.status = status
		if strings.HasPrefix(o.Header().Get("Content-Type"), "text/event-stream") {
			o.eventStream = true
		}
		o.wroteHeader = true
		o.ResponseWriter.WriteHeader(status)
	}
}

func (o *observedWriter) Write(p []byte) (int, error) {
	if !o.wroteHeader {
		o.WriteHeader(200)
	}
	n, err := o.ResponseWriter.Write(p)
	o.remember(p)
	return n, err
}

func (o *observedWriter) Flush() {
	if flusher, ok := o.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (o *observedWriter) remember(p []byte) {
	if len(p) > observedTail {
		p = p[len(p)-observedTail:]
	}
	o.tail = append(o.tail, p...)
	if len(o.tail) > observedTail {
		o.tail = o.tail[len(o.tail)-observedTail:]
	}
}

// terminalStream reports whether an SSE response ended with llama.cpp's
// [DONE] marker; truncated or failed streams never publish a snapshot.
func (o *observedWriter) terminalStream() bool {
	if !o.eventStream {
		return true
	}
	return bytes.Contains(o.tail, []byte("[DONE]"))
}

// trackingBody watches the upstream response body so mid-stream failures are
// visible to the coordinator after forwarding returns.
type trackingBody struct {
	io.ReadCloser
	observer *observedWriter
}

func (t *trackingBody) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 {
		t.observer.remember(p[:n])
	}
	if err == io.EOF {
		t.observer.sawEOF = true
	} else if err != nil {
		t.observer.streamError = err
	}
	return n, err
}
