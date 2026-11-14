# Known limitations

Deliberate scaffold boundaries — what opentambo does not do (yet):

- **Downloads** support public repositories and exact GGUF filenames. There is
  no Hugging Face browser, gated-repository credential handling, shard
  discovery, or upstream checksum verification. Add all shards for a split
  model yourself and launch the first one. Downloads are not automatically
  retried; failed jobs expose the error and can be resumed manually.
- **Vision models**: download the projector yourself and add `--mmproj` plus
  its absolute path as separate arguments.
- **GGUF inspection** shows filesystem metadata, not parsed
  architecture/tensor metadata.
- **Server panel** does not yet collect GPU memory/temperature, slot
  activity, or throughput, and does not persist server logs.
- **No automatic eviction, model loading, or LiteLLM registration.** LiteLLM
  remains a separate gateway; see [gateway.md](gateway.md).
- **No authentication.** This is by design: put the panel behind your own
  access policy and keep the listener on loopback.
- A user-started server already on a port is not adopted or controlled. Stop
  it yourself before launching another on that port, or choose a different
  port.
