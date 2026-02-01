const { defineConfig } = require('@playwright/test')

module.exports = defineConfig({
    testDir: 'tests/e2e',
    timeout: 60000,
    retries: 0,
    use: {
        baseURL: 'http://127.0.0.1:4174',
        viewport: { width: 1280, height: 720 },
        headless: true
    },
    webServer: {
        command: 'go run cmd/server/main.go -port 4174 -rtc-udp-port 50001 -admin-key test-key',
        url: 'http://127.0.0.1:4174',
        reuseExistingServer: false,
        timeout: 20000
    }
})
