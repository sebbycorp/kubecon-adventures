# AGENTS.md

Instructions for AI coding agents working in this repo.

## What this repo is

A collection of KubeCon demo/lab builds. Each top-level folder is a self-contained component that can be started on its own.

| Folder | What it is | Start it |
|--------|------------|----------|
| `vault/` | HashiCorp Vault 1.21 in Docker, file storage, UI on | `cd vault && ./init.sh` |

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
- Vault: `VAULT_ADDR=http://localhost:8200`. A container restart leaves Vault sealed; rerun `vault/init.sh` to unseal.
