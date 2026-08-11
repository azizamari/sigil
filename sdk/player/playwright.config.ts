import { defineConfig, devices } from "@playwright/test";

const PORT = 8099;

export default defineConfig({
  testDir: "./tests/e2e",
  testMatch: "**/*.spec.ts",
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  reporter: process.env.CI ? "line" : "list",
  use: {
    baseURL: `http://localhost:${PORT}`,
    ...devices["Desktop Chrome"],
  },
  webServer: {
    command: `node tests/e2e/server.mjs`,
    url: `http://localhost:${PORT}/__fetched`,
    reuseExistingServer: !process.env.CI,
    stdout: "pipe",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
