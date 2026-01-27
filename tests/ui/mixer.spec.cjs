const { test, expect } = require('@playwright/test')

async function joinRoom(page) {
    await page.addInitScript(() => {
        window.__TEST__ = true
    })
    await page.goto('/r/test-room')
    await page.fill('#nickname', 'Tester')
    await page.click('#btn-join')
    await expect(page.locator('#room-view')).toBeVisible()
}

test('mic gain slider updates debug state', async ({ page }) => {
    await joinRoom(page)
    const slider = page.locator('#mic-gain')
    await slider.evaluate((el) => {
        el.value = '160'
        el.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const micGain = await page.evaluate(() => window.__audioDebug?.micGain)
    expect(micGain).toBeCloseTo(1.6, 2)
})

test('peer volume slider updates debug state', async ({ page }) => {
    await joinRoom(page)
    const slider = page.locator('#peer-volume-peer-a .mixer-slider')
    await slider.evaluate((el) => {
        el.value = '180'
        el.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const peerGain = await page.evaluate(() => window.__audioDebug?.peerGains?.['peer-a'])
    expect(peerGain).toBeCloseTo(1.8, 2)
})

test.describe('mobile mixer panel', () => {
    test.use({ viewport: { width: 390, height: 844 } })

    test('toggle opens panel in portrait', async ({ page }) => {
        await joinRoom(page)
        const hasOpen = await page.evaluate(() => document.body.classList.contains('mixer-open'))
        expect(hasOpen).toBe(false)

        await page.click('#btn-mixer')
        const nowOpen = await page.evaluate(() => document.body.classList.contains('mixer-open'))
        expect(nowOpen).toBe(true)
    })
})
