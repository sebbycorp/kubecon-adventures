# Duck Hunt

The first arcade game. Every duck is a kagent agent session running as an Agent Substrate actor in a gVisor sandbox. The look is NES-style, with original pixel art. See [DESIGN.md](DESIGN.md) for the game design.

## Start

```bash
../vault/init.sh          # Vault unsealed (OpenAI key)
../kagent/setup.sh        # cluster, kagent + Substrate, agentgateway
./deploy.sh               # gateway routes, duck Agent, backend image, host port 8090
```

| Page | URL |
|------|-----|
| Phone (players scan the QR code) | http://localhost:8090/duck-hunt |
| Big screen (booth TV, add `?autostart` for kiosk mode) | http://localhost:8090/duck-hunt/screen |
| Admin (no sign-in yet, LAN only) | http://localhost:8090/admin |

`deploy.sh` can be rerun safely. It builds the image, pushes it to the local registry (`localhost:5001`) and rolls out a new version. To reach the game from other machines, run `sudo ufw route allow proto tcp from any to any port 8090`.

## How it fits together

- `manifests/gateway.yaml`: the agentgateway `arcade` Gateway, the front door for the game and the future Cloudflare Tunnel.
- `manifests/llm.yaml`: OpenAI behind agentgateway. The duck agents call it with a placeholder key, and the gateway injects the real key (`openai` Secret, from Vault). The route only matches the in-cluster hostname, so public traffic can't reach the LLM.
- `manifests/duck-agent.yaml`: the `duck` Agent (structured output `{pattern, speed, quack}`) on its own Harness and the `kagent-default` WorkerPool.
- `manifests/app.yaml`: the Go backend (one replica, game state in memory) and its HTTPRoute.
- `manifests/atelet-metrics.yaml`: a headless Service so the backend can read Substrate's own snapshot and restore timings from atelet.
- Backend:
  - `kagent.go`: kagent gRPC and A2A client
  - `game.go`: players, ducks, the Hunter, stats
  - `metrics.go`: atelet scraper
  - `main.go`: HTTP API and SSE
- `web/`: the pages, with no build step. `sprites.js` holds the pixel art and the synthesized sound effects.

## Run the backend locally

```bash
kubectl --context kind-kagent -n kagent port-forward svc/kagent-controller 8083:8083 &
go run . -addr :18090 -kagent-url http://localhost:8083 -atelet-metrics ""
```
