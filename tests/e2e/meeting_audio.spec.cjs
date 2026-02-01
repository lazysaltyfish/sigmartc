const { test, chromium } = require('@playwright/test')
const path = require('path')

const audioFiles = [
    path.join(__dirname, '..', 'fixtures', 'fake-audio-440.wav'),
    path.join(__dirname, '..', 'fixtures', 'fake-audio-554.wav'),
    path.join(__dirname, '..', 'fixtures', 'fake-audio-659.wav')
]

async function createClient(baseURL, roomId, name, audioPath) {
    const browser = await chromium.launch({
        headless: true,
        args: [
            '--use-fake-device-for-media-stream',
            '--use-fake-ui-for-media-stream',
            `--use-file-for-fake-audio-capture=${audioPath}`,
            '--autoplay-policy=no-user-gesture-required'
        ]
    })
    const context = await browser.newContext({
        baseURL,
        permissions: ['microphone']
    })
    await context.addInitScript(() => {
        window.__E2E__ = true
    })
    const page = await context.newPage()
    await page.goto(`/r/${roomId}`, { waitUntil: 'domcontentloaded' })
    await page.fill('#nickname', name)
    await page.click('#btn-join')
    await page.waitForSelector('#room-view:not(.hidden)', { timeout: 15000 })
    return { browser, context, page, name }
}

async function waitForPeers(page, expectedCount) {
    await page.waitForFunction((count) => {
        if (typeof window.__getPeerCount !== 'function') return false
        return window.__getPeerCount() >= count
    }, expectedCount, { timeout: 20000 })
}

async function waitForInboundAudio(page, expectedStreams) {
    await page.waitForFunction(async (count) => {
        const pc = window.__pc
        if (!pc || pc.connectionState !== 'connected') return false
        const stats = await pc.getStats()
        let inbound = 0
        let active = 0
        stats.forEach((report) => {
            if (report.type !== 'inbound-rtp') return
            const kind = report.kind || report.mediaType
            if (kind !== 'audio') return
            inbound += 1
            if ((report.packetsReceived || 0) > 5) {
                active += 1
            }
        })
        return inbound >= count && active >= count
    }, expectedStreams, { timeout: 30000 })
}

test('meeting room audio flows between multiple clients', async ({ baseURL }) => {
    test.setTimeout(60000)
    const roomId = `meeting-${Date.now()}`
    const clients = []

    try {
        for (let i = 0; i < 3; i += 1) {
            const client = await createClient(baseURL, roomId, `client-${i + 1}`, audioFiles[i])
            clients.push(client)
        }

        await Promise.all(clients.map((client) => waitForPeers(client.page, 2)))
        await Promise.all(clients.map((client) => waitForInboundAudio(client.page, 2)))
    } finally {
        await Promise.all(clients.map(async (client) => {
            await client.context.close().catch(() => {})
            await client.browser.close().catch(() => {})
        }))
    }
})
