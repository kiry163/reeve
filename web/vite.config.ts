import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// npm run dev 时把 /api 转发给 Go 后端（默认 127.0.0.1:18800）。
const api = process.env.REEVE_API || 'http://127.0.0.1:18800';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { target: 'es2022' },
  server: { port: 5173, proxy: { '/api': { target: api, changeOrigin: true } } },
});
