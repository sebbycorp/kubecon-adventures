# kubecon-adventures

KubeCon demo and lab builds. Each folder is a self-contained component.

| Component | Description |
|-----------|-------------|
| [vault](vault/) | HashiCorp Vault in Docker with persistent file storage and the web UI |
| [arcade](arcade/) | Agent arcade (Duck Hunt, Agent Flow, Substrate Siege, Cluster Defense, Gateway Defense, Flappy Substrate, Pod Packer, Substrate Selfie): every character is a kagent agent on Agent Substrate |
| [tunnel](tunnel/) | Cloudflare Tunnel giving the arcade a public HTTPS address |
| [gke](gke/) | The lab on a GKE Standard cluster on Google Cloud (Substrate Siege skipped by default) |
| [boot](boot/) | Brings the whole lab back after the host reboots |

Agent instructions live in [AGENTS.md](AGENTS.md) (`CLAUDE.md` imports it).

To run the lab on Google Cloud (GKE) instead of the local kind cluster, see [gke/](gke/README.md).
