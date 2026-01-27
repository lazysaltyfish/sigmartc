# Deployment (SSH + Docker)

## Prereqs
- Remote host with Docker installed and running.
- SSH access to the host.
- Remote host has `bash` and `tar` available (common on most Linux distros).
- UDP `50000` reachable from clients (firewall/security group).

## SSH deployment script

From the project root:

```bash
ADMIN_KEY=your-secret ./scripts/deploy_ssh.sh user@host
```

Optional settings:

```bash
PORT=8080 NETWORK_MODE=bridge UDP_PORT=50000 \
  ./scripts/deploy_ssh.sh user@host /opt/sigmartc

# Bind HTTP to all interfaces
PORT=8080 NETWORK_MODE=bridge HTTP_BIND_HOST=0.0.0.0 \
  ./scripts/deploy_ssh.sh user@host /opt/sigmartc
```

Environment variables used by the script:
- `ADMIN_KEY`: admin panel key passed to the server.
- `PORT`: HTTP port used by the server process (bridge mode maps host:PORT → container:PORT).
- `NETWORK_MODE`: `bridge` (default) or `host`.
- `UDP_PORT`: WebRTC UDP port (container + publish when `NETWORK_MODE=bridge`).
- `HTTP_BIND_HOST`: Host IP for HTTP port binding when `NETWORK_MODE=bridge` (default: `127.0.0.1`).
- `IMAGE_NAME`: Docker image name (default: `sigmartc`).
- `CONTAINER_NAME`: Docker container name (default: `sigmartc`).
- `SSH_OPTS`: extra ssh options (example: `-i ~/.ssh/id_rsa`).

WebRTC ICE is bound to a single UDP port (default `50000`). Make sure UDP `50000` is open in your firewall.
When using `NETWORK_MODE=host`, Docker cannot bind HTTP to `127.0.0.1`; use `bridge` if you need localhost-only HTTP.

If you run behind Caddy, keep the app bound to localhost or a private network so `X-Forwarded-For` can only come from the proxy.

## Local Docker build/run

```bash
docker build -t sigmartc .
```

Host networking (recommended):

```bash
docker run -d --name sigmartc \
  --restart unless-stopped \
  --network host \
  -e ADMIN_KEY=your-secret \
  -e PORT=8080 \
  -e RTC_UDP_PORT=50000 \
  -v sigmartc_data:/data \
  sigmartc
```

Bridge networking (may require additional UDP handling):

```bash
docker run -d --name sigmartc \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -p 50000:50000/udp \
  -e ADMIN_KEY=your-secret \
  -e PORT=8080 \
  -e RTC_UDP_PORT=50000 \
  -v sigmartc_data:/data \
  sigmartc
```

All-interfaces HTTP:

```bash
docker run -d --name sigmartc \
  --restart unless-stopped \
  -p 0.0.0.0:8080:8080 \
  -p 50000:50000/udp \
  -e ADMIN_KEY=your-secret \
  -e PORT=8080 \
  -e RTC_UDP_PORT=50000 \
  -v sigmartc_data:/data \
  sigmartc
```
