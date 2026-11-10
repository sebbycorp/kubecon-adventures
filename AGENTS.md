# AGENTS.md

Instructions for AI coding agents working in this repo.

## What this repo is

A collection of KubeCon demo/lab builds. Each top-level folder is a self-contained component that can be started on its own.

| Folder | What it is | Start it |
|--------|------------|----------|
| `vault/` | HashiCorp Vault 1.21 in Docker, file storage, UI on | `cd vault && ./init.sh` |
| `kagent/` | kind cluster with kagent 1.0 alpha, Agent Substrate, agentgateway | `cd kagent && ./setup.sh` |
| `arcade/` | Agent arcade (Duck Hunt, Agent Flow, Substrate Siege, Cluster Defense, Gateway Defense, Flappy Substrate, Pod Packer, Substrate Selfie): every character is a kagent agent on Agent Substrate (port 8090) | `cd arcade && ./deploy.sh` |
| `tunnel/` | Cloudflare Tunnel: public `https://kubecon.agentpwned.ai` to the arcade, cloudflared in the kind cluster, `/admin` blocked | `cd tunnel && ./setup.sh` |
| `homepage/` | Homepage dashboard for the host, cluster, and containers (port 3000) | `cd homepage && ./start.sh` |
| `gke/` | The same lab on a GKE Standard cluster (Substrate Siege skipped by default) | `cd gke && ./setup.sh` |
| `boot/` | Brings the cluster, every game, the tunnel, Vault and Homepage back after a reboot (systemd user unit) | `cd boot && ./install.sh` |

Add a row here when you add a new component.

## Redeploying on GKE

`gke/setup.sh` runs the lab on a GKE Standard cluster (see [gke/README.md](gke/README.md)); Substrate Siege is skipped by default (`SKIP_GAMES`). It has not been run end to end yet. What differs from kind, and where it's handled:

- **Cluster:** GKE Standard (Substrate's gVisor workers need node access Autopilot doesn't allow), 3 × `e2-standard-8` on demand, Ubuntu nodes, pod range `10.60.0.0/16`.
- **`arcade/deploy.sh` settings:** `PLATFORM=gke`, `KUBE_CONTEXT`, `IMAGE_REPO` (Artifact Registry), `POD_CIDR_PREFIX` (rewrites the `10.244.` in the four in-cluster-only AgentgatewayPolicies in `flow-policies.yaml`, `gatedef.yaml` and `selfie.yaml`), `SKIP_GAMES`, `HEIST_NODES`. Defaults keep the kind behaviour.
- **Private Gateway:** on GKE the arcade Gateway uses `gke/manifests/gateway-params.yaml` (ClusterIP Service), because a LoadBalancer Service would get a public IP past the tunnel's `/admin` block.
- **Disabled games:** the arcade reads `DISABLED_GAMES` (set from `SKIP_GAMES`): a disabled game isn't started, its routes 404, and the front page, TV and leaderboards hide it (`arcade/disabled.go`).
- **Tunnel:** `tunnel/setup.sh` takes `KUBE_CONTEXT`. Don't run cloudflared for the same tunnel in both kind and GKE (Cloudflare splits traffic between them); see gke/README.md.
- **Not needed on GKE:** `boot/`, `homepage/`, `vault/`.

**After deploying, check:**
- `https://kubecon.agentpwned.ai/` and `/tv` return 200 and `/admin` returns 404.
- `/api/stats` shows `workers` > 0 and the game pools filled (`flappy_pool`, `podPacker_pool`, `selfie_pool`, `duckHunt_pool`). If the arcade started before kagent was ready, restart it (`kubectl -n arcade rollout restart deploy/arcade`); if a small pool stays empty, restart that WorkerPool's deployment in `kagent` and then the arcade (see `boot/up.sh` for the same recovery).
- `/api/tokens` shows every deployed game's route (the `arcade-gw-metrics` Service in `arcade/manifests/gateway-metrics.yaml`).
- Rerun the booth load test (`go test -tags cluster -run TestBoothLoad` in `arcade/games/defense`, with `ARCADE_URL` set) to confirm capacity, CPU and token use on the new cluster.

## Conventions

- One folder per component, each with its own `README.md` and a single entry point script or compose file.
- Docker Compose for local services. Pin image versions (e.g. `hashicorp/vault:1.21`), never `latest`.
- These are lab setups: TLS off and single-key unseal are fine locally, but say so in the component README.

## Secrets

- Never commit secrets. `vault/.vault-keys.json` holds the Vault unseal key and root token and is gitignored.
- Do not print, cat, or copy root tokens or unseal keys into files, commits, or chat. Point the user to the script that produces them.

## Environment notes

- The host user is in the `docker` group, but a shell started before the group was added needs `newgrp docker <<< "<cmd>"` to reach the Docker socket.
- The host firewall is ufw with ufw-docker, so published container ports are blocked from other machines until opened with `sudo ufw route allow proto tcp from any to any port <port>`. Agents can't sudo; ask the user to run it.
- Public domain for the games: `kubecon.agentpwned.ai`, served through a Cloudflare Tunnel into the kind cluster. Leave the other `agentpwned.ai` records (apex, `www`, `lab`, `desk`) alone. Cloudflare credentials are in the lab Vault (`http://172.16.10.111:8200`, `secret/lab/cloudflare`); its address and token are in the local Vault at `secret/kubecon/lab-vault`.
- Vault: `VAULT_ADDR=http://localhost:8200`. A container restart leaves Vault sealed; rerun `vault/init.sh` to unseal.
