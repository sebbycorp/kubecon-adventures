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
| `boot/` | Brings the cluster, every game, the tunnel, Vault and Homepage back after a reboot (systemd user unit) | `cd boot && ./install.sh` |

Add a row here when you add a new component.

## Redeploying on GKE

Everything above targets a local kind cluster (`kind-kagent`) on one 8-CPU host. To run the same lab on Google Cloud, keep the components and change the kind-specific parts below. Nothing here has been run on GKE yet: verify each step.

**Cluster.** A GKE **Standard** cluster (not Autopilot): Agent Substrate runs its own gVisor worker pods (`ateworker-gvisor`), which need node access Autopilot doesn't allow. Sizing from the combined load test (about 80 bots on every game used 6–8 CPUs and 16–20 GB here): 3 × `e2-standard-8` is comfortable for a booth (about $105–125 for 5 days on demand), 2 × `e2-standard-8` is enough for normal traffic. Avoid Spot VMs for an event.

**Versions** (pinned in `kagent/setup.sh`): kagent `1.0.0-alpha17` (Agent Substrate `v0.6.0-alpha3`), agentgateway `v1.7.0`, Gateway API `v1.6.3`, model `gpt-4.1-mini`.

**Install order on GKE:**
1. `kagent/setup.sh`, minus the kind parts: skip the kind binary and `kind create cluster`, the local registry, MetalLB, the metrics-server `--kubelet-insecure-tls` patch (GKE ships metrics-server) and the `kagent-ui-port` socat container. Keep Gateway API, `kagent install` (it installs kagent and Agent Substrate into the current context) and the two agentgateway Helm charts.
2. `arcade/deploy.sh`, with these changes:
   - **Context:** it hard-codes `kubectl --context kind-kagent`. Point `k()` at the GKE context (as `tunnel/setup.sh` does too).
   - **Image registry:** it builds to `localhost:5001/arcade:<tag>`. Push to Artifact Registry instead (e.g. `us-central1-docker.pkg.dev/<project>/arcade/arcade:<tag>`) and let the nodes pull from it.
   - **OpenAI key:** it reads `OPENAI_API_KEY` from the local Vault unless the variable is set. On GKE, export it (or use Secret Manager); never commit it.
   - **Host port 8090:** the `arcade-port` socat container is for the local LAN. Drop it; players come in through the Cloudflare tunnel.
   - **Cluster facts:** `NODES`, `CPUS` and `MEMGIB` come from this host (`nproc`, `free`); set them to the GKE node values (they only feed Agent Flow's hints).
3. **In-cluster-only policies.** Four AgentgatewayPolicies allow only kind's pod network, `source.address.startsWith("10.244.")`: `arcade/manifests/flow-policies.yaml`, `gatedef.yaml` (two) and `selfie.yaml`. GKE's pod range differs (see `gcloud container clusters describe <name> --format='value(clusterIpv4Cidr)'`); change the prefix or the LLM routes will refuse every agent.
4. **Node names.** `arcade/main.go` maps Substrate Siege's city districts to `HEIST_NODES` (default `kagent-worker,kagent-worker2`). Add a `HEIST_NODES` env var (comma-separated GKE node names) to the arcade container in `arcade/manifests/app.yaml`, or the districts fall back to random ones.
5. `tunnel/setup.sh` (its `k()` is also `kind-kagent`; switch the context). cloudflared runs in the cluster and dials out, so no load balancer or firewall changes are needed, and `kubecon.agentpwned.ai` keeps working. `tunnel/access.sh on|off` is unchanged.
6. **Not needed on GKE:** `boot/` (GKE restarts nodes and pods itself), `homepage/` (it reads the local Docker and kind cluster) and `vault/` (unless you keep secrets there).

**After deploying, check:**
- `https://kubecon.agentpwned.ai/` and `/tv` return 200 and `/admin` returns 404.
- `/api/stats` shows `workers` > 0 and the game pools filled (`flappy_pool`, `podPacker_pool`, `selfie_pool`, `duckHunt_pool`). If the arcade started before kagent was ready, restart it (`kubectl -n arcade rollout restart deploy/arcade`); if a small pool stays empty, restart that WorkerPool's deployment in `kagent` and then the arcade (see `boot/up.sh` for the same recovery).
- `/api/tokens` shows every game's route (the `arcade-gw-metrics` Service in `arcade/manifests/gateway-metrics.yaml`).
- Rerun the booth load tests (`go test -tags cluster -run TestBoothLoad` in `arcade/games/heist` and `arcade/games/defense`, with `ARCADE_URL` set) to confirm capacity, CPU and token use on the new cluster.

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
