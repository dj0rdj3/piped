import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig(({ mode }) => ({
  define: { __PIPED_DEVELOPMENT__: mode === 'development' },
  plugins: [vue(), tailwindcss()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    proxy: {
      '/api/': { target: 'http://127.0.0.1:8080', ws: true },
      '/mcp': 'http://127.0.0.1:8080',
    },
  },
  preview: { host: '127.0.0.1' },
}));
