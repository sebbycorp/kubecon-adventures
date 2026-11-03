# Boot

Brings the whole lab back after the host reboots, with nobody logged in: the kind cluster (kagent, Agent Substrate, agentgateway), every arcade game, the Cloudflare tunnel, Vault and Homepage.

```bash
./install.sh   # once: boot service, linger, kind restart policy
```

`install.sh` can be rerun safely. It:

1. Sets the kind nodes and registry to `restart=unless-stopped`. kind creates its nodes with `restart=on-failure`, which a clean shutdown doesn't trigger, so after a reboot the cluster would stay down.
2. Installs the systemd user unit `kubecon-lab.service`, which runs `up.sh` at boot, and enables it.
3. Enables lingering for the user, so the unit starts at boot without a login.

`up.sh` is what runs at boot, and it is safe to run by hand at any time. It only starts what is stopped, and waits for what is starting:

| Step | Waits for |
|------|-----------|
| Docker | the daemon |
| Containers | starts any stopped: the kind nodes and registry, `vault`, `homepage`, `arcade-port` (8090), `kagent-ui-port` |
| Kubernetes | the API server, every node `Ready` |
| Deployments | a rollout of every Deployment in the lab's namespaces (kagent, ate-system, agentgateway-system, arcade, cloudflared, ...) |
| Vault | unseals it if sealed, from `vault/.vault-keys.json`, without printing anything. It doesn't run `vault/init.sh`, which prints the root token. |
| Homepage | `homepage/start.sh` (refreshes its kubeconfig) |
| Arcade | `http://localhost:8090` and every game page answering 200 |
| Public | `https://kubecon.agentpwned.ai` through the tunnel |

If anything isn't up by the end, it exits non-zero and systemd retries after 60 s.

```bash
systemctl --user start kubecon-lab.service      # run it now
journalctl --user -u kubecon-lab.service -f     # watch it
./install.sh --remove                           # remove the unit
```

The unit points at the `up.sh` of the checkout `install.sh` was run from, so run it from the checkout that stays on the machine.
