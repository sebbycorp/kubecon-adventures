# The lab on Google Cloud (GKE)

Runs the same platform as [`kagent/`](../kagent/) (kagent 1.0 alpha with Agent Substrate, agentgateway) on a GKE Standard cluster, then deploys the [arcade](../arcade/). **Substrate Siege is left out by default**; every other game is deployed.

Not yet run end to end on GKE. Check each step the first time.

## Start

```bash
gcloud auth login
gcloud config set project <your-project>
export OPENAI_API_KEY=...          # or OPENAI_SECRET=<Secret Manager secret name>
./setup.sh
```

`setup.sh` uses **your** gcloud login and project. It:

1. Enables the GKE and Artifact Registry APIs.
2. Creates the Artifact Registry repo `us-central1-docker.pkg.dev/<project>/arcade` and lets the nodes' service account pull from it.
3. Creates a zonal **GKE Standard** cluster `kubecon-arcade`: 3 × `e2-standard-8`, Ubuntu nodes, on-demand. It must be Standard because Substrate's gVisor workers need node access that Autopilot doesn't allow. Pod range `10.60.0.0/16`.
4. Installs the Gateway API CRDs, kagent and Substrate (`kagent install`) and agentgateway, at the versions pinned in `kagent/setup.sh`.
5. Runs `../arcade/deploy.sh` with `PLATFORM=gke`. It pushes the image to Artifact Registry, rewrites the in-cluster-only LLM policies for the GKE pod range, sets the node facts from the real nodes, and skips Substrate Siege.
6. Checks that the arcade Gateway's Service is **ClusterIP**. On GKE, a LoadBalancer Service would get a public IP, bypassing the tunnel's `/admin` block and exposing the LLM routes (and your OpenAI spend). `manifests/gateway-params.yaml` keeps it in-cluster.

The script can be rerun safely. Settings (all env vars) are listed at the top of `setup.sh`: `PROJECT`, `ZONE`, `NODE_COUNT`, `MACHINE_TYPE`, `SKIP_GAMES` (set `SKIP_GAMES=` to deploy Siege too), and others.

Try it before it's public:

```bash
kubectl -n agentgateway-system port-forward svc/arcade 8090:80   # http://localhost:8090/ and /admin
```

## Public URL (Cloudflare tunnel)

`TUNNEL=1 ./setup.sh` (or `KUBE_CONTEXT=<gke context> ../tunnel/setup.sh`) runs cloudflared in the GKE cluster. It reuses the `kubecon-arcade` tunnel and `kubecon.agentpwned.ai`. cloudflared connects outward, so no load balancer or firewall rule is needed.

**If the kind lab is still running its cloudflared, both clusters become connectors of the same tunnel, and Cloudflare splits players between them.** Pick one:

- Move the URL to GKE: `kubectl --context kind-kagent -n cloudflared scale deploy/cloudflared --replicas=0`, then run the tunnel against GKE.
- Run GKE side by side on its own name: `TUNNEL_NAME=kubecon-arcade-gke PUBLIC_HOST=gke.agentpwned.ai KUBE_CONTEXT=<gke context> ../tunnel/setup.sh`.

## Cost

Three `e2-standard-8` nodes on demand cost about $105–125 for 5 days in us-central1. On top of that come the zonal cluster management fee (covered by GKE's free tier for one zonal cluster), disks and Artifact Registry storage. Two nodes are enough outside a booth (`NODE_COUNT=2`).

## Tear down

```bash
./teardown.sh          # deletes the cluster (asks first)
ALL=1 ./teardown.sh    # also the Artifact Registry repo
```

## Not needed on GKE

`boot/` (GKE restarts nodes and pods itself), `homepage/` (it reads the local Docker and kind cluster), and `vault/` (use `OPENAI_SECRET` and Secret Manager instead). The kind-only parts of `kagent/setup.sh` are not used here: the local registry, MetalLB, the metrics-server patch, and the socat port containers.

## Lab-only caveats

The same caveats as the kind lab apply: kagent and Substrate are alpha, TLS is off inside the cluster, and the OpenAI key is a plain Kubernetes Secret.
