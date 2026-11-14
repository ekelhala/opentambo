import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig, loadEnv } from 'vite'

// The production build lands in internal/desk/web. The Go binary embeds it
// at build time (`make build`, i.e. go build -tags ui); the directory itself
// is git-ignored build output.
export default defineConfig(({ mode }) => {
  // loadEnv also picks up variables exported by the shell (e.g. via make),
  // which take precedence over the repo-root .env file.
  const env = loadEnv(mode, '..', 'OPENTAMBO_')
  const apiTarget = env.OPENTAMBO_API ?? `http://${env.OPENTAMBO_LISTEN ?? '127.0.0.1:8090'}`
  return {
    plugins: [react(), tailwindcss()],
    build: {
      outDir: '../internal/desk/web',
      emptyOutDir: true,
    },
    server: {
      // changeOrigin: false keeps the browser's Host header, so the backend's
      // same-origin check passes: the browser sees Vite as the origin.
      proxy: {
        '/api': { target: apiTarget, changeOrigin: false },
        '/v1': { target: apiTarget, changeOrigin: false },
      },
    },
    resolve: {
      alias: {
        '@': path.resolve(import.meta.dirname, 'src'),
      },
    },
  }
})
