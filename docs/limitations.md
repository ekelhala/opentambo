# Known limitations

These are deliberate boundaries — things OpenTambo does not do (yet):

- **Downloads** support public repositories and exact GGUF filenames. There's
  no Hugging Face browser, no gated-repo credential handling, no shard
  discovery, and no upstream checksum verification. Add all shards for a split
  model yourself and launch the first one. Downloads are not automatically
  retried — failed jobs show the error and you can resume them manually.
- **Vision models**: download the projector yourself and add `--mmproj` with
  its absolute path as separate arguments.
- **GGUF inspection** shows filesystem metadata only, not parsed
  architecture or tensor metadata.
- **Server panel** does not yet show GPU memory or temperature, slot
  activity, or throughput, and it does not persist server logs.
- **No automatic eviction, model loading, or LiteLLM registration.** LiteLLM
  stays a separate gateway; see [gateway.md](gateway.md).
- **No authentication.** This is by design: put the panel behind your own
  access policy and keep the listener on loopback.
- A server you started yourself on a port is not adopted or controlled. Stop
  it yourself before launching another on that port, or choose a different
  port.
