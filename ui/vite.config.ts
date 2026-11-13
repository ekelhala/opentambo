import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig, loadEnv } from 'vite'

// The production build lands in internal/desk/web, where the Go binary
// embeds it with //go:embed web/*.
export default defineConfig(({ mode }) => {
  // loadEnv also picks up variables exported by the shell (e.g. via make),
  // which take precedence over the repo-root .env file.
  const env = loadEnv(mode, '..', 'LLAMA_DESK_')
  const apiTarget = env.LLAMA_DESK_API ?? `http://${env.LLAMA_DESK_LISTEN ?? '127.0.0.1:8090'}`
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
