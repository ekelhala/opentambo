# opentambo web UI

The React + TypeScript + Vite control panel. The production build outputs to
`../internal/desk/web`, which the Go binary embeds when compiled with
`-tags ui` (`make build` does this for you).

```sh
bun install     # install dependencies
bun run dev     # dev server, proxies /api and /v1 to the backend
bun run build   # production build into ../internal/desk/web
bun run lint    # oxlint
```

See [../docs/development.md](../docs/development.md) for the full workflow.
