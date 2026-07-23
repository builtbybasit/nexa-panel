import { fileURLToPath, URL } from 'node:url'

import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  build: {
    outDir: '../internal/platform/webui/dist',
    emptyOutDir: true,
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      // Keep the browser's Host header so the API's Origin/Host CSRF
      // comparison passes in development (string shorthand implies
      // changeOrigin: true, which rewrites Host to the target).
      '/api': { target: 'http://127.0.0.1:8888', changeOrigin: false },
    },
  },
})
