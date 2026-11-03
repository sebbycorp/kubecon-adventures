# AGENTS.md

Instructions for AI coding agents working in this repo.

## What this repo is

A collection of KubeCon demo/lab builds. Each top-level folder is a self-contained component that can be started on its own.

| Folder | What it is | Start it |
|--------|------------|----------|
| `vault/` | HashiCorp Vault 1.21 in Docker, file storage, UI on | `cd vault && ./init.sh` |
| `kagent/` | kind cluster with kagent 1.0 alpha, Agent Substrate, agentgateway | `cd kagent && ./setup.sh` |
| `arcade/` | Agent arcade (Agent Hunt, Agent Flow, Substrate Siege, Cluster Defense, Gateway Defense): every character is a kagent agent on Agent Substrate (port 8090) | `cd arcade && ./deploy.sh` |
| `tunnel/` | Cloudflare Tunnel: public `https://kubecon.agentpwned.ai` to the arcade, cloudflared in the kind cluster, `/admin` blocked | `cd tunnel && ./setup.sh` |
| `homepage/` | Homepage dashboard for the host, cluster, and containers (port 3000) | `cd homepage && ./start.sh` |
| `boot/` | Brings the cluster, every game, the tunnel, Vault and Homepage back after a reboot (systemd user unit) | `cd boot && ./install.sh` |

Add a row here when you add a new component.

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
