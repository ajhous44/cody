const { defineConfig } = require('@playwright/test');
module.exports = defineConfig({
  testDir: '.',
  testMatch: '*.spec.js',
  workers: 1,
  timeout: 30000,
  use: { baseURL: 'http://127.0.0.1:4173', browserName: 'chromium', trace: 'retain-on-failure' },
  webServer: {
    command: 'go run ./server',
    url: 'http://127.0.0.1:4173/?token=browser-test',
    timeout: 120000,
    reuseExistingServer: false,
  },
});
