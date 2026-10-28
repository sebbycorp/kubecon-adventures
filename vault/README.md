# Vault

HashiCorp Vault 1.21 running in Docker with file storage and the web UI.

## Start

```bash
cp .env.example .env   # set VAULT_ROOT_TOKEN to the root token you want
./init.sh
```

If `VAULT_ROOT_TOKEN` is set when Vault is initialized for the first time, `init.sh` creates a root token with that ID and revokes the generated one. Leave it unset to keep Vault's random token. `.env` is gitignored.

On first run this initializes Vault with a single unseal key and writes the key and root token to `.vault-keys.json` (gitignored). On later runs it just starts and unseals. It prints the `VAULT_ADDR` / `VAULT_TOKEN` exports when done.

- UI: http://localhost:8200
- Data: Docker volume `vault_vault-data` (survives `docker compose down`; removed by `down -v`)

## Access from other machines

Vault listens on all interfaces (`0.0.0.0:8200`). On hosts running ufw with ufw-docker (Omarchy's default), container ports are firewalled, so open the port once:

```bash
sudo ufw route allow proto tcp from any to any port 8200
```

Use `from <your-lan>/24` instead of `from any` to limit access to your LAN. Then browse to `http://<machine-ip>:8200`.

## Files

- `docker-compose.yml`: the Vault service
- `config/vault.hcl`: server config (file storage, TCP listener on 8200)
- `init.sh`: start, init on first run, unseal

## Lab-only caveats

TLS is disabled and there is one unseal key. Don't use this setup outside a local lab.
The container healthcheck reports unhealthy while Vault is sealed.
