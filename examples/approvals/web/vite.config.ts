import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// `npm run dev` serves the UI on :5180 and sends /api to the Go backend, so
// the UI can be worked on with hot reload while `go run ./examples/approvals`
// runs. `npm run build` writes dist/, which the Go backend serves itself.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5180,
    proxy: { '/api': process.env.APPROVALS_BACKEND ?? 'http://localhost:8090' },
  },
});
