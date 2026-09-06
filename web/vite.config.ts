import { sveltekit } from "@sveltejs/kit/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [sveltekit()],
  server: {
    // Local development talks to a Go API started on 8080 (`go run ./cmd/api`
    // with the VYN_* variables set); production has Caddy in front (ADR 0008).
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
});
