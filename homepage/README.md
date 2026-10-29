# Homepage

[Homepage](https://gethomepage.dev/) v2.7.0 dashboard for the lab, at http://localhost:3000. It shows:

- Host CPU, RAM, disk and temperature
- The kind cluster: per-node CPU and RAM (from metrics-server), plus pod status for kagent, agentgateway and the Agent Substrate components, including the gVisor duck worker pool
- Docker containers: Vault, the kind nodes, the local registry and port forwarders
- Links to the kagent UI, Vault, the lab Vault, Cloudflare and the release pages

## Start

```bash
./start.sh
```

`start.sh` writes the kind cluster's kubeconfig to `config/kubeconfig` (gitignored), then runs `docker compose up -d`. Run it again after recreating the cluster. Homepage uses host networking so it can reach the kind API server on `127.0.0.1`.

Edit the files in `config/` (`services.yaml`, `widgets.yaml`, `bookmarks.yaml`, `settings.yaml`); Homepage reloads them on its own.

## Access from other machines

Homepage uses host networking, so ufw handles it as an ordinary host port, not a Docker route:

```bash
sudo ufw allow proto tcp from any to any port 3000
```

The kagent UI link points to host port 8080, which is a Docker-published port (see `../kagent/README.md`):

```bash
sudo ufw route allow proto tcp from any to any port 8080
```

If you open the dashboard by a hostname or IP other than `localhost`, `127.0.0.1`, `172.16.10.135` or `omarchy`, add it to `HOMEPAGE_ALLOWED_HOSTS` in `docker-compose.yml`.

## Lab-only caveats

The container mounts the Docker socket (read-only) and an admin kubeconfig for the kind cluster. Both give broad control of this host. Don't expose Homepage to the internet.
