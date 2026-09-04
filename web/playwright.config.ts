import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  use: {
    baseURL: "http://127.0.0.1:4173",
    ...devices["Desktop Chrome"],
  },
  webServer: {
    command: "pnpm build && pnpm exec vite preview --host 127.0.0.1",
    port: 4173,
    reuseExistingServer: !process.env.CI,
  },
});
