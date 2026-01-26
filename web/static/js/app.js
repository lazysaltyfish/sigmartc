const config = {
    iceServers: [{ urls: 'stun:stun.l.google.com:19302' }]
};

let localStream;
let pc;
let ws;
let myId;
let peers = new Map(); // peerId -> { name, element, pc? }
let isMuted = false;

const joinView = document.getElementById('join-view');
const roomView = document.getElementById('room-view');
const userList = document.getElementById('user-list');
const avatarGrid = document.getElementById('avatar-grid');
const audioContainer = document.getElementById('audio-container');

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

    try {
        localStream = await navigator.mediaDevices.getUserMedia({ audio: true });
        startSignaling(name);
        joinView.classList.add('hidden');
        roomView.classList.remove('hidden');
        setupVAD(localStream, 'self');
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
    
    // 4. Redirect to home
    window.location.href = '/';
}

document.getElementById('btn-mute').onclick = toggleMute;
document.getElementById('btn-copy').onclick = () => {
    navigator.clipboard.writeText(window.location.href);
    alert('链接已复制到剪贴板');
};

// 3. Signaling & WebRTC
function startSignaling(name) {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${window.location.host}/ws?room=${roomUUID}&name=${encodeURIComponent(name)}`);

    ws.onmessage = async (e) => {
        const msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'room_state':
                myId = msg.self_id;
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
                await pc.setRemoteDescription(new RTCSessionDescription({ type: 'offer', sdp: msg.sdp }));
                const answer = await pc.createAnswer();
                await pc.setLocalDescription(answer);
                ws.send(JSON.stringify({ type: 'answer', sdp: answer.sdp }));
                break;
            case 'answer':
                await pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: msg.sdp }));
                break;
            case 'candidate':
                await pc.addIceCandidate(new RTCIceCandidate(msg.candidate));
                break;
            case 'error':
                alert(msg.message);
                location.href = '/';
                break;
        }
    };
}

function initWebRTC() {
    pc = new RTCPeerConnection(config);
    
    localStream.getTracks().forEach(track => pc.addTrack(track, localStream));

    pc.onicecandidate = (e) => {
        if (e.candidate) {
            ws.send(JSON.stringify({ type: 'candidate', candidate: e.candidate }));
        }
    };

    pc.ontrack = (e) => {
        const stream = e.streams[0];
        const peerId = stream.id; // StreamID is now forced to be PeerID by the server
        
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
        
        setupVAD(stream, peerId);
    };

    pc.onnegotiationneeded = async () => {
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        ws.send(JSON.stringify({ type: 'offer', sdp: offer.sdp }));
    };
}

// 4. UI Helpers
function addPeer(id, name, animate) {
    if (id === myId || peers.has(id)) return;

    // Sidebar item
    const item = document.createElement('div');
    item.className = 'user-item active';
    item.id = `user-${id}`;
    item.innerText = name;
    userList.appendChild(item);

    // Avatar grid item
    const wrapper = document.createElement('div');
    wrapper.className = 'avatar-wrapper';
    wrapper.id = `avatar-wrap-${id}`;
    wrapper.innerHTML = `
        <div class="avatar" id="avatar-${id}">${name[0].toUpperCase()}</div>
        <div class="avatar-name">${name}</div>
    `;
    avatarGrid.appendChild(wrapper);

    peers.set(id, { name });
}

function removePeer(id) {
    document.getElementById(`user-${id}`)?.remove();
    document.getElementById(`avatar-wrap-${id}`)?.remove();
    document.getElementById(`audio-${id}`)?.remove();
    peers.delete(id);
}

const ICONS = {
    micOn: `<svg viewBox="0 0 24 24" width="24" height="24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z"></path><path d="M19 10v2a7 7 0 0 1-14 0v-2"></path><line x1="12" y1="19" x2="12" y2="23"></line><line x1="8" y1="23" x2="16" y2="23"></line></svg>`,
    micOff: `<svg viewBox="0 0 24 24" width="24" height="24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><line x1="1" y1="1" x2="23" y2="23"></line><path d="M9 9v3a3 3 0 0 0 5.12 2.12M15 9.34V4a3 3 0 0 0-5.94-.6"></path><path d="M17 16.95A7 7 0 0 1 5 12v-2m14 0v2a7 7 0 0 1-.11 1.23"></path><line x1="12" y1="19" x2="12" y2="23"></line><line x1="8" y1="23" x2="16" y2="23"></line></svg>`
};

function toggleMute() {
    isMuted = !isMuted;
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

function setupVAD(stream, peerId) {
    const audioContext = new (window.AudioContext || window.webkitAudioContext)();
    const source = audioContext.createMediaStreamSource(stream);
    const analyser = audioContext.createAnalyser();
    analyser.fftSize = 256; 
    source.connect(analyser);

    const bufferLength = analyser.frequencyBinCount;
    const dataArray = new Uint8Array(bufferLength);
    
    // Create self avatar if not exists
    if (peerId === 'self') {
        peerId = myId; 
        if (!document.getElementById(`avatar-${peerId}`)) {
             const name = document.getElementById('nickname').value;
             const wrapper = document.createElement('div');
             wrapper.className = 'avatar-wrapper';
             wrapper.innerHTML = `
                <div class="avatar" id="avatar-${peerId}">${name[0].toUpperCase()}</div>
                <div class="avatar-name">${name} (你)</div>
            `;
            avatarGrid.appendChild(wrapper);
        }
    }

    function check() {
        if (!peers.has(peerId) && peerId !== myId) {
             // If peer avatar is gone, stop checking
            if(!document.getElementById(`avatar-${peerId}`)) {
                // Ideally close AudioContext here, but for simplicity we just stop the loop
                return;
            }
        }

        analyser.getByteFrequencyData(dataArray);
        let sum = 0;
        for (let i = 0; i < bufferLength; i++) sum += dataArray[i];
        const average = sum / bufferLength;
        
        const avatar = document.getElementById(`avatar-${peerId}`);
        if (avatar) {
            // Threshold 10 filters minimal background noise
            if (average > 10) {
                avatar.classList.add('speaking');
                // Dynamic effect: Scale 1.0 -> 1.2
                const scale = 1 + Math.min((average - 10) / 100, 0.2);
                avatar.style.transform = `scale(${scale})`;
                // Dynamic shadow opacity/size
                avatar.style.boxShadow = `0 0 0 ${4 + (average/8)}px rgba(59, 165, 93, 0.6)`;
            } else {
                avatar.classList.remove('speaking');
                avatar.style.transform = 'scale(1)';
                avatar.style.boxShadow = 'none';
            }
        }
        requestAnimationFrame(check);
    }

    check();
}
