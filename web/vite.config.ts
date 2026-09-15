import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': 'http://localhost:20130',
      '/v1': 'http://localhost:20130',
      '/usage': 'http://localhost:20130',
      '/translator': 'http://localhost:20130',
      '/debug': 'http://localhost:20130',
    },
  },
})
