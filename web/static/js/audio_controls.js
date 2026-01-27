;(function (root, factory) {
    if (typeof module === 'object' && module.exports) {
        module.exports = factory()
    } else {
        root.AudioControls = factory()
    }
})(typeof self !== 'undefined' ? self : this, function () {
    const MAX_PERCENT = 200
    const MIN_PERCENT = 0

    function clampPercent(value) {
        const num = Number(value)
        if (Number.isNaN(num)) return 100
        return Math.min(MAX_PERCENT, Math.max(MIN_PERCENT, num))
    }

    function percentToGain(percent) {
        return clampPercent(percent) / 100
    }

    function gainToPercent(gain) {
        const num = Number(gain)
        if (Number.isNaN(num)) return 100
        return clampPercent(Math.round(num * 100))
    }

    return {
        MAX_PERCENT,
        MIN_PERCENT,
        clampPercent,
        percentToGain,
        gainToPercent
    }
})
