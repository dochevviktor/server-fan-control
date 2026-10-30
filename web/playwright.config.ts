import { defineConfig } from '@playwright/test';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

process.env.NVIDIA_FAN_E2E_CONTROL_FILE ??= join(tmpdir(), `nvidia-fan-e2e-${process.pid}.pid`);

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  timeout: 35_000,
  use: {
    baseURL: 'http://127.0.0.1:18080',
    browserName: 'chromium',
    ...(process.env.PLAYWRIGHT_CHROME_PATH ? { launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROME_PATH } } : {}),
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure'
  },
  webServer: {
    command: 'node tests/start-backend.mjs',
    url: 'http://127.0.0.1:18080/healthz',
    timeout: 120_000,
    reuseExistingServer: false
  }
});
