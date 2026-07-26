import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    // 接入真实后端后启用代理并删除 src/mocks:
    // proxy: {
    //   '/api': 'http://127.0.0.1:9090',
    //   '/livez': 'http://127.0.0.1:9090',
    //   '/readyz': 'http://127.0.0.1:9090',
    // },
  },
})
