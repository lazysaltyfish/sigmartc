(() => {
    const params = new URLSearchParams(window.location.search);
    const key = params.get('key') || '';

    const statsEl = document.getElementById('stats');
    const logsEl = document.getElementById('logs');
    const banInput = document.getElementById('ban-ip');
    const banBtn = document.getElementById('ban-btn');

    function fetchJSON(url, fallbackEl) {
        return fetch(url)
            .then((res) => res.json())
            .catch(() => {
                if (fallbackEl) {
                    fallbackEl.textContent = '加载失败';
                }
                return null;
            });
    }

    if (statsEl) {
        fetchJSON(`/admin?action=stats&key=${encodeURIComponent(key)}`, statsEl)
            .then((data) => {
                if (data) {
                    statsEl.textContent = JSON.stringify(data, null, 2);
                }
            });
    }

    if (logsEl) {
        fetchJSON(`/admin?action=logs&key=${encodeURIComponent(key)}`, logsEl)
            .then((data) => {
                if (Array.isArray(data)) {
                    logsEl.textContent = data.join('\n');
                }
            });
    }

    if (banBtn && banInput) {
        banBtn.addEventListener('click', () => {
            const ip = banInput.value.trim();
            if (!ip) return;
            fetch(`/admin?action=ban&ip=${encodeURIComponent(ip)}&key=${encodeURIComponent(key)}`, {
                method: 'POST'
            }).then(() => location.reload());
        });
    }
})();
