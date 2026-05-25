# Local LiveKit Server (dev)

Brings up a single-node LiveKit SFU + Redis for end-to-end testing the aloqa
calls foundation (ALK-628 / ALK-629).

## Start

```bash
docker compose -f scripts/livekit/docker-compose.dev.yml up -d
```

Then export the following in your aloqa-claude `.env` (or shell):

```bash
export LIVEKIT_URL=ws://localhost:7880
export LIVEKIT_API_KEY=APIaloqaDev
export LIVEKIT_API_SECRET=aloqa-livekit-development-secret-32bytes-min
```

The webhook is delivered to `http://host.docker.internal:8090/livekit/webhook`
which assumes aloqa-claude listens on the default `0.0.0.0:8090` (SERVER_PORT).
Adjust `livekit.yaml` if your local server uses a different port.

## Verify

```bash
docker compose -f scripts/livekit/docker-compose.dev.yml logs -f livekit-server
```

Expect `started LiveKit server` and `Redis connected` entries. The SFU listens
on:

- `ws://localhost:7880` — signaling
- `tcp/7881` — ICE-TCP fallback
- `udp/50000-50100` — media

## Stop

```bash
docker compose -f scripts/livekit/docker-compose.dev.yml down
```
