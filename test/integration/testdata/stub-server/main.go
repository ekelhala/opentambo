// Command stub-server is a deliberately crippled llama.cpp stand-in used
// only by the integration suite's fault-injection scenario: it answers
// /health, /props and /slots (with NO usable slot 0) so the prompt-cache
// capability probe must classify the build as unsupported, and it serves a
// canned /v1/chat/completions response so the test can prove inference keeps
// working uncached. The real reuse evidence runs against the pinned real
// llama-server build, never this stub.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
)

func main() {
	port := 0
	args := os.Args[1:]
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--port" {
			port, _ = strconv.Atoi(args[i+1])
		}
	}
	if port == 0 {
		log.Fatal("stub-server requires --port")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /props", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"build_info": "stub-without-slot-snapshots", "total_slots": 1, "webui": false})
	})
	// No slot 0: save/restore cannot exist on this build.
	mux.HandleFunc("GET /slots", func(w http.ResponseWriter, _ *http.Request) { write(w, []any{}) })
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{
			"id": "stub", "object": "chat.completion", "created": 0, "model": "stub",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "STUB-REPLY"},
			}},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"stub has no such endpoint"}`, http.StatusNotFound)
	})
	log.Fatal(http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), mux))
}

func write(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
