import { fileURLToPath, URL } from 'node:url'
import { searchForWorkspaceRoot } from 'vite'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { breakpointSpecificity } from '@go-tangra/ui/vite'
import { federation } from '@module-federation/vite'
import { remoteConfig } from './module-federation.config'

// The hr UI is a federated remote served by the module under /ui/ and
// relayed by the gateway at /m/hr/; `vite` alone runs a standalone dev
// shell (src/main.ts) against the gateway.
export default defineConfig({
  base: '/m/hr/',
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  plugins: [vue(), tailwindcss(), breakpointSpecificity(), federation(remoteConfig)],
  server: {
    proxy: { '/api': { target: 'https://localhost:8443', secure: false, changeOrigin: false } },
    // The icon test reads the Go manifest's nav entries (only that directory).
    fs: {
      allow: [
        searchForWorkspaceRoot(process.cwd()),
        fileURLToPath(new URL('../pkg/hrmanifest', import.meta.url)),
      ],
    },
  },
  build: { outDir: 'dist', emptyOutDir: true, sourcemap: false, target: 'esnext' },
  test: {
    environment: 'jsdom',
    environmentOptions: { jsdom: { url: 'https://localhost/hr' } },
    include: ['tests/unit/**/*.spec.ts'],
    setupFiles: ['tests/unit/setup.ts'],
    server: { deps: { inline: ['@go-tangra/ui'] } },
  },
})
