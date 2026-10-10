import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The built app is served by the Go binary on :4096. In dev, Vite serves the
// UI and proxies the API to the same backend.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    port: 5173,
    proxy: { '/api': 'http://127.0.0.1:4096' },
  },
})
