# LiteLLM gateway

opentambo exposes one OpenAI-compatible entry point for all managed
processes, so LiteLLM backend URLs stay fixed when models, ports, or child
servers change:

```text
Client → LiteLLM (keys and policies) → opentambo /v1 → selected llama-server
```

## API model names

Set **API model name** in the launch form, for example `qwen3.8-27b`. It is
saved with the model's launch settings and passed to the child with `--alias`.
Active names must be unique, and the name stays stable when you change the
child's port.

## Routes

On opentambo's normal listener (default `127.0.0.1:8090`):

- `GET /v1/models` — model names for currently running managed processes.
- `POST /v1/chat/completions`, `/v1/completions`, `/v1/embeddings`,
  `/v1/responses` — routed by the JSON `model` field to a managed process.

Requests preserve their JSON payload, including custom parameters. Responses,
HTTP error statuses, and SSE streaming pass through. A disconnected caller
cancels the upstream request. Gateway authorization headers and cookies are
removed before forwarding to a child. There are no automatic retries.
Requests are limited to 32 MiB; the proxy waits up to ten minutes for response
headers and imposes no total stream duration.

There is no implicit startup or fallback: start the desired model in the UI
first. A stopped or unknown model returns an OpenAI-style HTTP 404; an
unreachable child returns 502; a child still loading can return its own 503.
Process names appear in `/v1/models` while loading — listing is not a
readiness guarantee. Backend support determines whether a particular
inference endpoint works.

## LiteLLM config

Use the same API base for every local model route:

```yaml
model_list:
  - model_name: qwen3.8-27b
    litellm_params:
      model: openai/qwen3.8-27b
      api_base: http://127.0.0.1:8090/v1
      api_key: local-opentambo
```

The key is a dummy provider key; opentambo does not authenticate. Retain
LiteLLM's existing database, client keys, and access controls. Model names
and access policies still need registration in LiteLLM; opentambo does not
synchronize its model list into LiteLLM's database.
