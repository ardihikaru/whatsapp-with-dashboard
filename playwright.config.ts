import { defineConfig, devices } from "@playwright/test";

// The suite boots its OWN app instance on 127.0.0.1:8801 through
// scripts/e2e-env.sh, which puts the app tables in a throwaway SQLite file.
// Nothing here touches MySQL — the only reachable MySQL is the shared
// production server.
const BASE_URL = process.env.WSM_E2E_BASE_URL ?? "http://127.0.0.1:8801";

// Real phone-portrait geometry. 390x844 is an iPhone 12/13/14; 360x740 is the
// small-Android floor and the width the layout is most likely to break at.
const PHONE = { width: 390, height: 844 };
const PHONE_SMALL = { width: 360, height: 740 };

export default defineConfig({
  testDir: "./e2e",
  outputDir: "./test-results",
  fullyParallel: false, // one app instance, one in-memory session store
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [["list"], ["html", { open: "never" }]],

  use: {
    baseURL: BASE_URL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",
  },

  projects: [
    {
      // Signs in once and saves the cookie. Separate project because its trace
      // and screenshots are FORCED OFF: it is the only place the plaintext
      // admin password is ever typed, and a DOM snapshot could capture it.
      name: "setup",
      testMatch: /auth\.setup\.ts/,
      use: { trace: "off", screenshot: "off", video: "off" },
    },
    {
      name: "mobile-portrait",
      dependencies: ["setup"],
      use: {
        ...devices["Desktop Chrome"],
        viewport: PHONE,
        isMobile: true,
        hasTouch: true,
        deviceScaleFactor: 3,
        storageState: "e2e/.auth/admin.json",
      },
    },
    {
      name: "mobile-small",
      dependencies: ["setup"],
      use: {
        ...devices["Desktop Chrome"],
        viewport: PHONE_SMALL,
        isMobile: true,
        hasTouch: true,
        deviceScaleFactor: 3,
        storageState: "e2e/.auth/admin.json",
      },
    },
    {
      // Regression guard: the drawer must not leak onto desktop.
      name: "desktop",
      dependencies: ["setup"],
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1280, height: 800 },
        storageState: "e2e/.auth/admin.json",
      },
    },
  ],

  webServer: {
    command: "./scripts/e2e-env.sh",
    // /api/v1/health is public by design (it is the container healthcheck), so
    // readiness needs no credential.
    url: `${BASE_URL}/api/v1/health`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
