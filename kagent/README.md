# kagent + Agent Substrate + agentgateway

A local Kubernetes platform for the agent arcade games. It runs a kind cluster on this machine with:

| Component | Version | Namespace |
|-----------|---------|-----------|
| kind node (Kubernetes) | v1.35.8 | |
| kagent | 1.0.0-alpha17 | `kagent` |
| Agent Substrate (installed by `kagent install`) | 0.6.0-alpha3 | `ate-system`, pod-cert namespace |
| agentgateway (OSS) | v1.7.0 | `agentgateway-system` |
| Gateway API CRDs | v1.6.3 | |
| MetalLB (LoadBalancer IPs on the `kind` Docker network) | v0.15.3 | `metallb-system` |
| metrics-server (`kubectl top`, Homepage node stats) | v0.9.0 | `kube-system` |

The cluster has one control-plane node and two workers. All three share this host's CPU and RAM, with no per-node limits. Agents run as Substrate actors in gVisor sandboxes. Idle agents are snapshotted to object storage and restored on demand.

## Start

```bash
../vault/init.sh   # Vault must be unsealed; the OpenAI key is read from secret/kubecon/api-keys
./setup.sh
```

`setup.sh` installs pinned `kind`, `kubectl`, `helm` and `kagent` CLIs into `~/.local/bin`, then sets up a local image registry (`localhost:5001`), the kind cluster, MetalLB, the Gateway API CRDs, kagent with Substrate, agentgateway, metrics-server, and host port 8080 for the kagent UI. You can run it again safely. To skip Vault, export `OPENAI_API_KEY` first.

- kubectl context: `kind-kagent`
- kagent UI: http://localhost:8080. A NodePort (30080) is published on host port 8080 by the `kagent-ui-port` socat container. To reach it from other machines, run `sudo ufw route allow proto tcp from any to any port 8080`.
- Smoke test: `kubectl apply -f manifests/smoke-test.yaml` creates a `kagent` Harness on the Substrate worker pool and a `hello-duck` Agent
- Push game images: `docker push localhost:5001/<image>`, then reference `localhost:5001/<image>` in manifests

## Tear down

```bash
kind delete cluster --name kagent
docker rm -f kind-registry kagent-ui-port
```

## Lab-only caveats

kagent and Substrate are alpha builds. TLS is off inside the cluster, and the OpenAI key is stored as a plain Kubernetes Secret in the `kagent` namespace. Don't use this setup outside a local lab.
