const { defineConfig } = require('@playwright/test')

module.exports = defineConfig({
    testDir: 'tests/ui',
    timeout: 30000,
    retries: 0,
    use: {
        baseURL: 'http://127.0.0.1:4173',
        viewport: { width: 1280, height: 720 },
        headless: true
    },
    webServer: {
        command: 'node tests/ui/static_server.cjs',
        url: 'http://127.0.0.1:4173',
        reuseExistingServer: !process.env.CI,
        timeout: 10000
    }
})
