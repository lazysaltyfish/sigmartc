const test = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')

const controls = require(path.join(__dirname, '..', '..', 'web', 'static', 'js', 'audio_controls.js'))

test('clampPercent clamps values to 0..200', () => {
    assert.equal(controls.clampPercent(-5), 0)
    assert.equal(controls.clampPercent(0), 0)
    assert.equal(controls.clampPercent(100), 100)
    assert.equal(controls.clampPercent(200), 200)
    assert.equal(controls.clampPercent(250), 200)
})

test('percentToGain maps percent to gain', () => {
    assert.equal(controls.percentToGain(0), 0)
    assert.equal(controls.percentToGain(100), 1)
    assert.equal(controls.percentToGain(150), 1.5)
    assert.equal(controls.percentToGain(250), 2)
})

test('gainToPercent maps gain to percent', () => {
    assert.equal(controls.gainToPercent(0), 0)
    assert.equal(controls.gainToPercent(1), 100)
    assert.equal(controls.gainToPercent(1.25), 125)
    assert.equal(controls.gainToPercent(2.5), 200)
})
