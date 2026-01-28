const config = {
    iceServers: [{ urls: 'stun:stun.l.google.com:19302' }]
};

const audioControls = window.AudioControls || {
    MAX_PERCENT: 200,
    MIN_PERCENT: 0,
    clampPercent: (value) => {
        const num = Number(value);
        if (Number.isNaN(num)) return 100;
        return Math.min(200, Math.max(0, num));
    },
    percentToGain: (percent) => {
        const num = Number(percent);
        if (Number.isNaN(num)) return 1;
        const clamped = Math.min(200, Math.max(0, num));
        return clamped / 100;
    },
    gainToPercent: (gain) => {
        const num = Number(gain);
        if (Number.isNaN(num)) return 100;
        return Math.min(200, Math.max(0, Math.round(num * 100)));
    }
};

const { MAX_PERCENT, clampPercent, percentToGain } = audioControls;
const isTestMode = Boolean(window.__TEST__);
const SFX_FILES = {
    join: '/static/audio/join.wav',
    leave: '/static/audio/leave.wav'
};
const SFX_VOLUME = 0.35;
const sfxBank = new Map();
let sfxPrimed = false;
const supportsContextSink = Boolean(window.AudioContext && typeof AudioContext.prototype.setSinkId === 'function');
const supportsElementSink = Boolean(window.HTMLMediaElement && typeof HTMLMediaElement.prototype.setSinkId === 'function');
let preferredOutputDeviceId = 'default';
let preferredInputDeviceId = 'default';

function primeSfx() {
    if (sfxPrimed || typeof Audio !== 'function') return;
    sfxPrimed = true;
    Object.entries(SFX_FILES).forEach(([key, url]) => {
        const audio = new Audio(url);
        audio.preload = 'auto';
        audio.volume = SFX_VOLUME;
        sfxBank.set(key, audio);
    });
}

function playSfx(key) {
    if (isTestMode) return;
    primeSfx();
    const base = sfxBank.get(key);
    if (!base) return;
    const clip = base.cloneNode();
    clip.volume = SFX_VOLUME;
    if (supportsElementSink && preferredOutputDeviceId) {
        const sinkPromise = clip.setSinkId(preferredOutputDeviceId);
        if (sinkPromise && typeof sinkPromise.catch === 'function') {
            sinkPromise.catch(() => { });
        }
    }
    const playPromise = clip.play();
    if (playPromise && typeof playPromise.catch === 'function') {
        playPromise.catch(() => { });
    }
}
const audioDebug = isTestMode ? { micGain: 1, peerGains: {} } : null;
if (audioDebug) {
    window.__audioDebug = audioDebug;
}

let localRawStream;
let localStream;
let localAudioContext;
let localGainNode;
let remoteAudioContext;
let testToneContext;
let pc;
let ws;
let myId;
let peers = new Map(); // peerId -> { name, volumePercent, gainNode, audioEl, sourceNode }
let isMuted = false;
let mixerOpen = false;
let localName = '';
let isLeaving = false;
let notifiedDisconnect = false;
let vadAudioContext;
let pendingSelfVAD = null;
const vadState = new Map();
let makingOffer = false;
let ignoreOffer = false;
const isPolite = true;

const joinView = document.getElementById('join-view');
const roomView = document.getElementById('room-view');
const userList = document.getElementById('user-list');
const avatarGrid = document.getElementById('avatar-grid');
const audioContainer = document.getElementById('audio-container');
const mixerPanel = document.getElementById('mixer-panel');
const micGainInput = document.getElementById('mic-gain');
const micGainValue = document.getElementById('mic-gain-value');
const playbackDeviceSelect = document.getElementById('playback-device');
const playbackHelp = document.getElementById('playback-help');
const inputDeviceSelect = document.getElementById('input-device');
const inputHelp = document.getElementById('input-help');
const peerVolumeList = document.getElementById('peer-volume-list');
const peerVolumeEmpty = document.getElementById('peer-volume-empty');
const btnMixer = document.getElementById('btn-mixer');

// --- Network Stats Manager ---
class NetworkStatsManager {
    constructor(pc) {
        this.pc = pc;
        this.intervalId = null;
        this.el = document.getElementById('network-status');
        this.elRtt = document.getElementById('net-rtt');
        this.elLoss = document.getElementById('net-loss');
        this.elContainer = document.getElementById('network-status');
    }

    start() {
        if (this.intervalId) return;
        this.el.classList.remove('hidden');
        this.intervalId = setInterval(() => this._updateStats(), 2000);
        this._updateStats(); // Initial call
    }

    stop() {
        if (this.intervalId) {
            clearInterval(this.intervalId);
            this.intervalId = null;
        }
        this.el.classList.add('hidden');
        this.elRtt.textContent = '-- ms';
        this.elLoss.textContent = '--% 丢包';
        this._updateUIClass('status-good'); // Reset color
    }

    async _updateStats() {
        if (!this.pc) return;
        const iceState = this.pc.iceConnectionState;
        if (iceState !== 'connected' && iceState !== 'completed') return;

        try {
            const stats = await this.pc.getStats();
            let rtt = null;
            let packetsLost = 0;
            let packetsReceived = 0;

            stats.forEach(report => {
                if (report.type === 'candidate-pair' && report.state === 'succeeded' && report.currentRoundTripTime !== undefined) {
                    rtt = report.currentRoundTripTime * 1000; // s to ms
                }
                if (report.type === 'inbound-rtp' && report.kind === 'audio') {
                    packetsLost += (report.packetsLost || 0);
                    packetsReceived += (report.packetsReceived || 0);
                }
            });

            // Calculate Loss Percentage (Simple approximation based on total accumulation)
            // Note: For instantaneous loss, we'd need to track delta. 
            // But standard WebRTC stats usually give cumulative.
            // Let's stick to cumulative for simplicity or implement delta if needed.
            // Actually, for a real-time indicator, delta is better. Let's try to do delta.

            // However, implementing delta requires state. Let's just use what we have first.
            // Refinement: We need previous values to calculate interval loss.

            let lossRate = 0;
            if (this._prevPacketsReceived !== undefined && this._prevPacketsLost !== undefined) {
                const deltaLost = packetsLost - this._prevPacketsLost;
                const deltaReceived = packetsReceived - this._prevPacketsReceived;
                const totalDetails = deltaLost + deltaReceived;
                if (totalDetails > 0) {
                    lossRate = (deltaLost / totalDetails) * 100;
                }
            }

            this._prevPacketsLost = packetsLost;
            this._prevPacketsReceived = packetsReceived;

            this._updateUI(rtt, lossRate);

        } catch (e) {
            console.warn('Failed to get stats:', e);
        }
    }

    _updateUI(rtt, lossRate) {
        // RTT Display
        const rttVal = rtt !== null ? Math.round(rtt) : '--';
        this.elRtt.textContent = `${rttVal} ms`;

        // Loss Display
        const lossVal = lossRate.toFixed(1);
        this.elLoss.textContent = `${lossVal}% 丢包`;

        // Determine Status
        let status = 'status-good';
        if ((rtt !== null && rtt > 300) || lossRate > 5) {
            status = 'status-bad';
        } else if ((rtt !== null && rtt > 100) || lossRate > 1) {
            status = 'status-ok';
        }

        this._updateUIClass(status);
    }

    _updateUIClass(statusClass) {
        this.elContainer.classList.remove('status-good', 'status-ok', 'status-bad');
        this.elContainer.classList.add(statusClass);
    }
}

let netStatsManager = null;

// 1. Initialize URL and View
const path = window.location.pathname;
let roomUUID = path.startsWith('/r/') ? path.substring(3) : '';
if (!roomUUID) {
    roomUUID = Math.random().toString(36).substring(2, 10);
    window.history.replaceState(null, '', `/r/${roomUUID}`);
}
document.getElementById('room-info').innerText = `即将进入房间: ${roomUUID}`;
document.getElementById('display-room-id').innerText = `房间: ${roomUUID}`;

// 2. Interaction Handlers
document.getElementById('btn-join').onclick = async () => {
    const name = document.getElementById('nickname').value.trim();
    if (!name) return alert('请输入昵称');
    primeSfx();

    try {
        const audioConstraints = buildAudioConstraints(preferredInputDeviceId);
        const stream = isTestMode ? await createTestToneStream() : await navigator.mediaDevices.getUserMedia({ audio: audioConstraints });
        handleJoin(name, stream);
    } catch (e) {
        alert('无法访问麦克风: ' + e.message);
    }
};

document.getElementById('btn-leave').onclick = () => {
    if (confirm("确定要离开房间吗？")) {
        leaveRoom();
    }
};

function leaveRoom() {
    if (isLeaving) return;
    isLeaving = true;
    notifiedDisconnect = true;

    if (netStatsManager) {
        netStatsManager.stop();
        netStatsManager = null;
    }

    // 1. Close WebRTC
    if (pc) {
        pc.close();
        pc = null;
    }
    // 2. Close WebSocket
    if (ws) {
        ws.close();
        ws = null;
    }
    // 3. Stop Local Media (Microphone)
    if (localStream) {
        localStream.getTracks().forEach(track => track.stop());
        localStream = null;
    }
    if (localRawStream) {
        localRawStream.getTracks().forEach(track => track.stop());
        localRawStream = null;
    }
    if (localAudioContext) {
        localAudioContext.close();
        localAudioContext = null;
    }
    if (remoteAudioContext) {
        remoteAudioContext.close();
        remoteAudioContext = null;
    }
    if (testToneContext) {
        testToneContext.close();
        testToneContext = null;
    }
    clearAllVAD();
    if (vadAudioContext) {
        vadAudioContext.close();
        vadAudioContext = null;
    }
    pendingSelfVAD = null;
    peers.forEach((_, id) => cleanupPeerAudio(id));
    peers.clear();
    setMixerOpen(false);

    // 4. Redirect to home
    window.location.href = '/';
}

document.getElementById('btn-mute').onclick = toggleMute;
if (btnMixer) {
    btnMixer.onclick = () => {
        setMixerOpen(!mixerOpen);
        resumeAudioContexts();
    };
}
document.addEventListener('pointerdown', (event) => {
    if (!mixerOpen) return;
    const target = event.target;
    if (!(target instanceof Element)) return;
    if (target.closest('#mixer-panel') || target.closest('#btn-mixer')) return;
    setMixerOpen(false);
});
if (micGainInput) {
    micGainInput.addEventListener('input', () => {
        setMicGain(micGainInput.value);
    });
    setMicGain(micGainInput.value);
}
document.getElementById('btn-copy').onclick = () => {
    navigator.clipboard.writeText(window.location.href);
    alert('链接已复制到剪贴板');
};

initPlaybackDevices();
initInputDevices();

function handleJoin(name, rawStream) {
    localName = name;
    isLeaving = false;
    notifiedDisconnect = false;
    localRawStream = rawStream;
    localStream = setupLocalAudio(rawStream);
    setMicGain(micGainInput?.value ?? 100);
    getRemoteAudioContext();
    resumeAudioContexts();
    refreshPlaybackDevices();
    refreshInputDevices();
    setTimeout(refreshInputDevices, 200);

    joinView.classList.add('hidden');
    roomView.classList.remove('hidden');
    if (isTestMode) {
        myId = 'test-self';
    }

    queueSelfVAD(localStream, name);
    setMixerOpen(!isMobilePortrait());
    syncMixerForViewport();

    if (isTestMode) {
        seedTestPeers();
        return;
    }

    startSignaling(name);
}

window.addEventListener('resize', () => {
    if (!roomView.classList.contains('hidden')) {
        syncMixerForViewport();
    }
});

function setupLocalAudio(rawStream) {
    localAudioContext = new (window.AudioContext || window.webkitAudioContext)();
    const source = localAudioContext.createMediaStreamSource(rawStream);
    localGainNode = localAudioContext.createGain();
    localGainNode.gain.value = percentToGain(micGainInput?.value ?? 100);
    const destination = localAudioContext.createMediaStreamDestination();
    source.connect(localGainNode).connect(destination);
    return destination.stream;
}

function createTestToneStream() {
    testToneContext = new (window.AudioContext || window.webkitAudioContext)();
    const oscillator = testToneContext.createOscillator();
    const gain = testToneContext.createGain();
    gain.gain.value = 0.0005;
    const destination = testToneContext.createMediaStreamDestination();
    oscillator.connect(gain).connect(destination);
    oscillator.start();
    return destination.stream;
}

function resumeAudioContexts() {
    if (localAudioContext && localAudioContext.state === 'suspended') {
        localAudioContext.resume();
    }
    if (remoteAudioContext && remoteAudioContext.state === 'suspended') {
        remoteAudioContext.resume();
    }
    if (testToneContext && testToneContext.state === 'suspended') {
        testToneContext.resume();
    }
    if (vadAudioContext && vadAudioContext.state === 'suspended') {
        vadAudioContext.resume();
    }
}

function setPlaybackHelp(message) {
    if (!playbackHelp) return;
    playbackHelp.textContent = message || '';
    playbackHelp.style.display = message ? 'block' : 'none';
}

function setInputHelp(message) {
    if (!inputHelp) return;
    inputHelp.textContent = message || '';
    inputHelp.style.display = message ? 'block' : 'none';
}

function loadPreferredPlaybackDevice() {
    try {
        const stored = localStorage.getItem('playbackDeviceId');
        if (stored) preferredOutputDeviceId = stored;
    } catch (e) {
        // Ignore storage access issues.
    }
}

function persistPreferredPlaybackDevice() {
    try {
        localStorage.setItem('playbackDeviceId', preferredOutputDeviceId);
    } catch (e) {
        // Ignore storage access issues.
    }
}

function loadPreferredInputDevice() {
    try {
        const stored = localStorage.getItem('inputDeviceId');
        if (stored) preferredInputDeviceId = stored;
    } catch (e) {
        // Ignore storage access issues.
    }
}

function persistPreferredInputDevice() {
    try {
        localStorage.setItem('inputDeviceId', preferredInputDeviceId);
    } catch (e) {
        // Ignore storage access issues.
    }
}

function buildAudioConstraints(deviceId) {
    if (!deviceId || deviceId === 'default') return true;
    return { deviceId: { exact: deviceId } };
}

function applyOutputDeviceToContext(deviceId) {
    if (!supportsContextSink || !remoteAudioContext || !deviceId) return;
    remoteAudioContext.setSinkId(deviceId).then(() => {
        setPlaybackHelp('');
    }).catch(() => {
        setPlaybackHelp('无法切换播放设备，请检查权限或设备状态');
    });
}

function setPreferredPlaybackDevice(deviceId) {
    if (!deviceId) return;
    preferredOutputDeviceId = deviceId;
    persistPreferredPlaybackDevice();
    applyOutputDeviceToContext(deviceId);
}

function setPreferredInputDevice(deviceId) {
    if (!deviceId) return;
    preferredInputDeviceId = deviceId;
    persistPreferredInputDevice();
    if (localStream && !isLeaving) {
        switchInputDevice(deviceId);
    }
}

async function refreshPlaybackDevices() {
    if (!playbackDeviceSelect) return;
    if (!navigator.mediaDevices || !navigator.mediaDevices.enumerateDevices) {
        playbackDeviceSelect.disabled = true;
        setPlaybackHelp('当前浏览器不支持枚举播放设备');
        return;
    }
    let devices = [];
    try {
        devices = await navigator.mediaDevices.enumerateDevices();
    } catch (e) {
        playbackDeviceSelect.disabled = true;
        setPlaybackHelp('无法读取播放设备');
        return;
    }
    const outputs = devices.filter((device) => device.kind === 'audiooutput');
    playbackDeviceSelect.innerHTML = '';
    if (outputs.length === 0) {
        playbackDeviceSelect.disabled = true;
        setPlaybackHelp('未检测到播放设备');
        return;
    }

    outputs.forEach((device, index) => {
        const option = document.createElement('option');
        option.value = device.deviceId || 'default';
        option.textContent = device.label || `播放设备 ${index + 1}`;
        playbackDeviceSelect.appendChild(option);
    });

    if (!supportsContextSink) {
        playbackDeviceSelect.disabled = true;
        setPlaybackHelp('当前浏览器不支持切换播放设备');
        return;
    }

    playbackDeviceSelect.disabled = false;
    setPlaybackHelp('');

    let nextId = preferredOutputDeviceId;
    if (!outputs.some((device) => device.deviceId === nextId)) {
        const defaultDevice = outputs.find((device) => device.deviceId === 'default');
        nextId = defaultDevice?.deviceId || outputs[0].deviceId;
    }
    playbackDeviceSelect.value = nextId;
    setPreferredPlaybackDevice(nextId);
}

async function refreshInputDevices() {
    if (!inputDeviceSelect) return;
    if (!navigator.mediaDevices || !navigator.mediaDevices.enumerateDevices) {
        inputDeviceSelect.disabled = true;
        setInputHelp('当前浏览器不支持枚举输入设备');
        return;
    }
    let devices = [];
    try {
        devices = await navigator.mediaDevices.enumerateDevices();
    } catch (e) {
        inputDeviceSelect.disabled = true;
        setInputHelp('无法读取输入设备');
        return;
    }
    const inputs = devices.filter((device) => device.kind === 'audioinput');
    inputDeviceSelect.innerHTML = '';

    // Even if inputs.length > 0, check if they have valid labels (permissions granted)
    // Some browsers return devices but with empty labels before permission
    const hasValidInputs = inputs.some((device) => device.label && device.label.length > 0);

    if (inputs.length === 0 || !hasValidInputs) {
        // Try to seed from current stream as fallback
        if (seedInputDeviceFromStream()) {
            return;
        }
        // If we have devices but no labels, show generic option
        if (inputs.length > 0) {
            inputs.forEach((device, index) => {
                const option = document.createElement('option');
                option.value = device.deviceId || 'default';
                option.textContent = `输入设备 ${index + 1}`;
                inputDeviceSelect.appendChild(option);
            });
            inputDeviceSelect.disabled = false;
            setInputHelp('');
            inputDeviceSelect.value = inputs[0].deviceId || 'default';
            return;
        }
        inputDeviceSelect.disabled = true;
        setInputHelp('未检测到输入设备');
        return;
    }

    // Build the device options
    inputs.forEach((device, index) => {
        const option = document.createElement('option');
        option.value = device.deviceId || 'default';
        option.textContent = device.label || `输入设备 ${index + 1}`;
        inputDeviceSelect.appendChild(option);
    });

    // Also add the current stream device if not already in the list
    if (localRawStream) {
        const track = localRawStream.getAudioTracks()[0];
        if (track) {
            const settings = typeof track.getSettings === 'function' ? track.getSettings() : {};
            const streamDeviceId = settings.deviceId;
            const streamLabel = track.label;
            if (streamDeviceId && !inputs.some((d) => d.deviceId === streamDeviceId)) {
                const option = document.createElement('option');
                option.value = streamDeviceId;
                option.textContent = streamLabel || '当前输入设备';
                inputDeviceSelect.appendChild(option);
            }
        }
    }

    inputDeviceSelect.disabled = false;
    setInputHelp('');

    let nextId = preferredInputDeviceId;
    if (!inputs.some((device) => device.deviceId === nextId)) {
        const defaultDevice = inputs.find((device) => device.deviceId === 'default');
        nextId = defaultDevice?.deviceId || inputs[0].deviceId;
    }
    inputDeviceSelect.value = nextId;
    setPreferredInputDevice(nextId);
}

function seedInputDeviceFromStream() {
    if (!inputDeviceSelect || !localRawStream) return false;
    const track = localRawStream.getAudioTracks()[0];
    if (!track) return false;
    const settings = typeof track.getSettings === 'function' ? track.getSettings() : {};
    const deviceId = settings.deviceId || 'default';
    const label = track.label || '当前输入设备';
    const option = document.createElement('option');
    option.value = deviceId;
    option.textContent = label;
    inputDeviceSelect.appendChild(option);
    inputDeviceSelect.disabled = false;
    setInputHelp('');
    preferredInputDeviceId = deviceId;
    persistPreferredInputDevice();
    inputDeviceSelect.value = deviceId;
    return true;
}

function initPlaybackDevices() {
    if (!playbackDeviceSelect) return;
    loadPreferredPlaybackDevice();
    playbackDeviceSelect.addEventListener('change', () => {
        setPreferredPlaybackDevice(playbackDeviceSelect.value);
    });
    if (navigator.mediaDevices && navigator.mediaDevices.addEventListener) {
        navigator.mediaDevices.addEventListener('devicechange', refreshPlaybackDevices);
    }
    refreshPlaybackDevices();
}

function initInputDevices() {
    if (!inputDeviceSelect) return;
    loadPreferredInputDevice();
    inputDeviceSelect.addEventListener('change', () => {
        setPreferredInputDevice(inputDeviceSelect.value);
    });
    if (navigator.mediaDevices && navigator.mediaDevices.addEventListener) {
        navigator.mediaDevices.addEventListener('devicechange', refreshInputDevices);
    }
    refreshInputDevices();
}

async function switchInputDevice(deviceId) {
    if (isTestMode) return;
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
        setInputHelp('当前浏览器不支持切换输入设备');
        return;
    }
    const constraints = buildAudioConstraints(deviceId);
    let newRawStream;
    try {
        newRawStream = await navigator.mediaDevices.getUserMedia({ audio: constraints });
    } catch (e) {
        setInputHelp('无法切换输入设备，请检查权限');
        return;
    }

    const oldRawStream = localRawStream;
    const oldLocalStream = localStream;
    const oldAudioContext = localAudioContext;

    localRawStream = newRawStream;
    localStream = setupLocalAudio(newRawStream);
    setMicGain(micGainInput?.value ?? 100);
    resumeAudioContexts();

    if (pc) {
        const newTrack = localStream.getAudioTracks()[0];
        if (newTrack) {
            const senders = pc.getSenders().filter((sender) => sender.track && sender.track.kind === 'audio');
            if (senders.length > 0) {
                await Promise.all(senders.map((sender) => sender.replaceTrack(newTrack)));
            } else {
                pc.addTrack(newTrack, localStream);
            }
        }
    }

    queueSelfVAD(localStream, localName);

    if (oldRawStream) {
        oldRawStream.getTracks().forEach((track) => track.stop());
    }
    if (oldLocalStream) {
        oldLocalStream.getTracks().forEach((track) => track.stop());
    }
    if (oldAudioContext) {
        oldAudioContext.close();
    }

    setInputHelp('');
}

function getRemoteAudioContext() {
    if (!remoteAudioContext) {
        remoteAudioContext = new (window.AudioContext || window.webkitAudioContext)();
        applyOutputDeviceToContext(preferredOutputDeviceId);
    }
    return remoteAudioContext;
}

function isMobilePortrait() {
    const portraitQuery = window.matchMedia('(max-width: 720px) and (orientation: portrait)');
    if (portraitQuery.matches) return true;
    const narrowQuery = window.matchMedia('(max-width: 720px)');
    return narrowQuery.matches && window.innerHeight >= window.innerWidth;
}

function setMixerOpen(open) {
    mixerOpen = Boolean(open);
    document.body.classList.toggle('mixer-open', mixerOpen);
    btnMixer?.classList.toggle('active', mixerOpen);
}

function syncMixerForViewport() {
    if (isMobilePortrait()) {
        if (mixerOpen) setMixerOpen(false);
    }
}

function setMicGain(percent) {
    const clamped = clampPercent(percent);
    if (micGainInput) micGainInput.value = clamped;
    if (micGainValue) micGainValue.textContent = `${clamped}%`;
    if (localGainNode) {
        localGainNode.gain.value = percentToGain(clamped);
    }
    if (audioDebug) {
        audioDebug.micGain = percentToGain(clamped);
    }
}

function getVADContext() {
    if (!vadAudioContext) {
        vadAudioContext = new (window.AudioContext || window.webkitAudioContext)();
    }
    return vadAudioContext;
}

function queueSelfVAD(stream, name) {
    pendingSelfVAD = { stream, name };
    maybeStartSelfVAD();
}

function maybeStartSelfVAD() {
    if (!pendingSelfVAD || !myId) return;
    const { stream, name } = pendingSelfVAD;
    pendingSelfVAD = null;
    setupVAD(stream, myId, name, true);
}

function getDisplayInitial(name) {
    const trimmed = (name || '').trim();
    if (!trimmed) return '?';
    return Array.from(trimmed)[0].toUpperCase();
}

function ensureAvatar(id, name, isSelf) {
    const existing = document.getElementById(`avatar-wrap-${id}`);
    if (existing) {
        const avatar = existing.querySelector('.avatar');
        const label = existing.querySelector('.avatar-name');
        if (avatar && name) {
            avatar.textContent = getDisplayInitial(name);
        }
        if (label && name) {
            label.textContent = isSelf ? `${name} (你)` : name;
        }
        return;
    }

    const wrapper = document.createElement('div');
    wrapper.className = 'avatar-wrapper';
    wrapper.id = `avatar-wrap-${id}`;

    const avatar = document.createElement('div');
    avatar.className = 'avatar';
    avatar.id = `avatar-${id}`;
    avatar.textContent = getDisplayInitial(name);

    const label = document.createElement('div');
    label.className = 'avatar-name';
    label.textContent = isSelf ? `${name} (你)` : name;

    wrapper.append(avatar, label);
    avatarGrid.appendChild(wrapper);
}

function cleanupVAD(peerId) {
    const state = vadState.get(peerId);
    if (!state) return;
    if (state.rafId) {
        cancelAnimationFrame(state.rafId);
    }
    if (state.source) state.source.disconnect();
    if (state.analyser) state.analyser.disconnect();
    vadState.delete(peerId);
}

function clearAllVAD() {
    Array.from(vadState.keys()).forEach((peerId) => cleanupVAD(peerId));
}

function updatePeerVolumeEmptyState() {
    if (!peerVolumeEmpty || !peerVolumeList) return;
    peerVolumeEmpty.style.display = peerVolumeList.children.length === 0 ? 'block' : 'none';
}

function seedTestPeers() {
    addPeer('peer-a', '测试A', false);
    addPeer('peer-b', '测试B', false);
    updatePeerVolumeEmptyState();
}

// 3. Signaling & WebRTC
function startSignaling(name) {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${window.location.host}/ws?room=${roomUUID}&name=${encodeURIComponent(name)}`);

    ws.onclose = () => {
        if (!notifiedDisconnect) {
            handleSocketFailure('连接已断开');
        }
    };
    ws.onerror = () => {
        if (!notifiedDisconnect) {
            handleSocketFailure('连接失败');
        }
    };

    ws.onmessage = async (e) => {
        const msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'room_state':
                myId = msg.self_id;
                maybeStartSelfVAD();
                msg.peers.forEach(p => addPeer(p.id, p.name, false));
                initWebRTC();
                break;
            case 'peer_join':
                addPeer(msg.peer.id, msg.peer.name, true);
                break;
            case 'peer_leave':
                removePeer(msg.peer_id);
                break;
            case 'offer':
                {
                    const offer = new RTCSessionDescription({ type: 'offer', sdp: msg.sdp });
                    const offerCollision = makingOffer || pc.signalingState !== 'stable';
                    ignoreOffer = !isPolite && offerCollision;
                    if (ignoreOffer) {
                        return;
                    }
                    if (offerCollision) {
                        await pc.setLocalDescription({ type: 'rollback' });
                    }
                    await pc.setRemoteDescription(offer);
                    const answer = await pc.createAnswer();
                    await pc.setLocalDescription(answer);
                    ws.send(JSON.stringify({ type: 'answer', sdp: answer.sdp }));
                }
                break;
            case 'answer':
                if (ignoreOffer) {
                    return;
                }
                await pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: msg.sdp }));
                break;
            case 'candidate':
                await pc.addIceCandidate(new RTCIceCandidate(msg.candidate));
                break;
            case 'error':
                handleSocketFailure(msg.message || '连接已断开');
                break;
        }
    };
}

function handleSocketFailure(message) {
    if (notifiedDisconnect) return;
    notifiedDisconnect = true;
    alert(message);
    leaveRoom();
}

function initWebRTC() {
    pc = new RTCPeerConnection(config);
    makingOffer = false;
    ignoreOffer = false;

    // Start network stats manager immediately
    if (!netStatsManager) {
        netStatsManager = new NetworkStatsManager(pc);
        netStatsManager.start();
    }

    localStream.getTracks().forEach(track => pc.addTrack(track, localStream));

    pc.onicecandidate = (e) => {
        if (e.candidate) {
            ws.send(JSON.stringify({ type: 'candidate', candidate: e.candidate }));
        }
    };

    pc.ontrack = (e) => {
        const stream = e.streams[0];
        const peerId = stream.id || e.track.id; // StreamID is now forced to be PeerID by the server

        // Create audio element
        let audio = document.getElementById('audio-' + peerId);
        if (!audio) {
            audio = document.createElement('audio');
            audio.id = 'audio-' + peerId;
            audio.autoplay = true;
            audio.playsInline = true;
            audioContainer.appendChild(audio);
        }
        audio.srcObject = stream;
        const playPromise = audio.play();
        if (playPromise && typeof playPromise.catch === 'function') {
            playPromise.catch(() => { });
        }
        attachRemoteAudio(peerId, audio);
        resumeAudioContexts();

        setupVAD(stream, peerId);
    };



    pc.onnegotiationneeded = async () => {
        try {
            makingOffer = true;
            if (pc.signalingState !== 'stable') {
                return;
            }
            const offer = await pc.createOffer();
            await pc.setLocalDescription(offer);
            if (ws && ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify({ type: 'offer', sdp: offer.sdp }));
            }
        } finally {
            makingOffer = false;
        }
    };
}

// 4. UI Helpers
function addPeer(id, name, animate) {
    if (id === myId || peers.has(id)) return;
    const safeName = (name || '').trim() || '匿名';

    // Sidebar item
    const item = document.createElement('div');
    item.className = 'user-item active';
    item.id = `user-${id}`;
    item.textContent = safeName;
    userList.appendChild(item);

    ensureAvatar(id, safeName, false);

    peers.set(id, { name: safeName, volumePercent: 100 });
    addPeerVolumeControl(id, safeName);
    updatePeerVolumeEmptyState();
    if (animate) {
        playSfx('join');
    }
}

function removePeer(id) {
    const hadPeer = peers.has(id);
    document.getElementById(`user-${id}`)?.remove();
    document.getElementById(`avatar-wrap-${id}`)?.remove();
    document.getElementById(`audio-${id}`)?.remove();
    removePeerVolumeControl(id);
    cleanupVAD(id);
    cleanupPeerAudio(id);
    peers.delete(id);
    updatePeerVolumeEmptyState();
    if (hadPeer) {
        playSfx('leave');
    }
}

const ICONS = {
    micOn: `<svg viewBox="0 0 24 24" width="24" height="24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z"></path><path d="M19 10v2a7 7 0 0 1-14 0v-2"></path><line x1="12" y1="19" x2="12" y2="23"></line><line x1="8" y1="23" x2="16" y2="23"></line></svg>`,
    micOff: `<svg viewBox="0 0 24 24" width="24" height="24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><line x1="1" y1="1" x2="23" y2="23"></line><path d="M9 9v3a3 3 0 0 0 5.12 2.12M15 9.34V4a3 3 0 0 0-5.94-.6"></path><path d="M17 16.95A7 7 0 0 1 5 12v-2m14 0v2a7 7 0 0 1-.11 1.23"></path><line x1="12" y1="19" x2="12" y2="23"></line><line x1="8" y1="23" x2="16" y2="23"></line></svg>`
};

function toggleMute() {
    isMuted = !isMuted;
    if (!localStream) return;
    const tracks = localStream.getAudioTracks();
    if (tracks.length > 0) {
        tracks[0].enabled = !isMuted;
    }

    const btn = document.getElementById('btn-mute');
    btn.innerHTML = isMuted ? ICONS.micOff : ICONS.micOn;

    // Toggle visual style: Red background when muted
    btn.classList.toggle('btn-danger', isMuted);

    document.getElementById(`avatar-${myId}`)?.classList.toggle('muted', isMuted);
}

function addPeerVolumeControl(peerId, name) {
    if (!peerVolumeList) return;
    if (document.getElementById(`peer-volume-${peerId}`)) return;

    const row = document.createElement('div');
    row.className = 'mixer-row';
    row.id = `peer-volume-${peerId}`;

    const label = document.createElement('span');
    label.className = 'mixer-label';
    label.textContent = name;

    const slider = document.createElement('input');
    slider.className = 'mixer-slider';
    slider.type = 'range';
    slider.min = '0';
    slider.max = String(MAX_PERCENT);
    slider.value = '100';
    slider.step = '1';
    slider.setAttribute('data-peer-id', peerId);
    slider.setAttribute('aria-label', `${name} 音量`);

    const value = document.createElement('span');
    value.className = 'mixer-value';
    value.textContent = '100%';

    slider.addEventListener('input', () => {
        setPeerVolume(peerId, slider.value, value);
    });

    row.append(label, slider, value);
    peerVolumeList.appendChild(row);
    setPeerVolume(peerId, slider.value, value);
}

function removePeerVolumeControl(peerId) {
    document.getElementById(`peer-volume-${peerId}`)?.remove();
    if (audioDebug && audioDebug.peerGains) {
        delete audioDebug.peerGains[peerId];
    }
}

function setPeerVolume(peerId, percent, valueEl) {
    const clamped = clampPercent(percent);
    if (valueEl) valueEl.textContent = `${clamped}%`;
    const peer = peers.get(peerId);
    if (peer) {
        peer.volumePercent = clamped;
        if (peer.gainNode) {
            peer.gainNode.gain.value = percentToGain(clamped);
        }
    }
    if (audioDebug) {
        audioDebug.peerGains[peerId] = percentToGain(clamped);
    }
}

function attachRemoteAudio(peerId, audioEl) {
    let peer = peers.get(peerId);
    if (!peer) {
        peer = { name: peerId, volumePercent: 100 };
        peers.set(peerId, peer);
        addPeerVolumeControl(peerId, peerId);
        updatePeerVolumeEmptyState();
    }

    peer.audioEl = audioEl;
    if (peer.sourceNode || peer.gainNode) {
        return;
    }

    const ctx = getRemoteAudioContext();
    const source = ctx.createMediaElementSource(audioEl);
    const gainNode = ctx.createGain();
    const percent = peer.volumePercent ?? 100;
    gainNode.gain.value = percentToGain(percent);
    source.connect(gainNode).connect(ctx.destination);

    peer.sourceNode = source;
    peer.gainNode = gainNode;

    if (audioDebug) {
        audioDebug.peerGains[peerId] = gainNode.gain.value;
    }
}

function cleanupPeerAudio(peerId) {
    const peer = peers.get(peerId);
    if (!peer) return;
    if (peer.sourceNode) peer.sourceNode.disconnect();
    if (peer.gainNode) peer.gainNode.disconnect();
    if (peer.audioEl) peer.audioEl.srcObject = null;
    peer.sourceNode = null;
    peer.gainNode = null;
    peer.audioEl = null;
}

function setupVAD(stream, peerId, displayName, isSelf) {
    const name = displayName || peers.get(peerId)?.name || peerId || '匿名';
    ensureAvatar(peerId, name, Boolean(isSelf));
    cleanupVAD(peerId);

    const audioContext = getVADContext();
    if (audioContext.state === 'suspended') {
        audioContext.resume();
    }
    const source = audioContext.createMediaStreamSource(stream);
    const analyser = audioContext.createAnalyser();
    analyser.fftSize = 256;
    source.connect(analyser);

    const bufferLength = analyser.frequencyBinCount;
    const dataArray = new Uint8Array(bufferLength);

    const state = { source, analyser, rafId: 0 };
    vadState.set(peerId, state);

    function check() {
        if (!vadState.has(peerId)) {
            return;
        }

        const avatar = document.getElementById(`avatar-${peerId}`);
        if (!avatar) {
            cleanupVAD(peerId);
            return;
        }

        analyser.getByteFrequencyData(dataArray);
        let sum = 0;
        for (let i = 0; i < bufferLength; i++) sum += dataArray[i];
        const average = sum / bufferLength;

        // Threshold 10 filters minimal background noise
        if (average > 10) {
            avatar.classList.add('speaking');
            // Dynamic effect: Scale 1.0 -> 1.2
            const scale = 1 + Math.min((average - 10) / 100, 0.2);
            avatar.style.transform = `scale(${scale})`;
            // Dynamic shadow opacity/size
            avatar.style.boxShadow = `0 0 0 ${4 + (average / 8)}px rgba(59, 165, 93, 0.6)`;
        } else {
            avatar.classList.remove('speaking');
            avatar.style.transform = 'scale(1)';
            avatar.style.boxShadow = 'none';
        }

        state.rafId = requestAnimationFrame(check);
    }

    check();
}
