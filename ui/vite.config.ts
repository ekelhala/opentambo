import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The production build lands in internal/desk/web, where the Go binary
// embeds it with //go:embed web/*.
const apiTarget = process.env.LLAMA_DESK_API ?? 'http://127.0.0.1:8090'
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../internal/desk/web',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': apiTarget,
      '/v1': apiTarget,
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, 'src'),
    },
  },
})
