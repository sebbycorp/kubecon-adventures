# Vault

HashiCorp Vault 1.21 running in Docker with file storage and the web UI.

## Start

```bash
./init.sh
```

On first run this initializes Vault with a single unseal key and writes the key and root token to `.vault-keys.json` (gitignored). On later runs it just starts and unseals. It prints the `VAULT_ADDR` / `VAULT_TOKEN` exports when done.

- UI: http://localhost:8200
- Data: Docker volume `vault_vault-data` (survives `docker compose down`; removed by `down -v`)

## Files

- `docker-compose.yml`: the Vault service
- `config/vault.hcl`: server config (file storage, TCP listener on 8200)
- `init.sh`: start, init on first run, unseal

## Lab-only caveats

TLS is disabled and there is one unseal key. Don't use this setup outside a local lab.
The container healthcheck reports unhealthy while Vault is sealed.
