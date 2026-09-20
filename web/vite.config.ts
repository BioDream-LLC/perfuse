import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The built assets are embedded into the Go binary, so they go to a directory
// the Go build can see. Relative asset paths keep it working when the UI is
// served from a subpath.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: './',
  build: {
    // Built into the Go package that embeds it, so `go build` alone produces a
    // binary with the interface in it and no separate asset directory to deploy.
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // In development the front end runs on its own port and proxies the API, so
    // cookies still work without CORS.
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8443',
        changeOrigin: false,
      },
    },
  },
  test: {
    // The e2e directory holds Playwright specs, which are a different runner.
    // Without this, vitest picks up their test.describe() calls and fails the
    // whole suite with an error that says nothing about the actual cause.
    // Run them with `make e2e`.
    // e2e-saml is excluded for a second reason as well as being a different runner: it talks to containers. A check that needs
    // Docker is a check people stop running, so make check has to pass without it.
    exclude: ['e2e/**', 'e2e-saml/**', 'node_modules/**', 'dist/**'],
  },
})
