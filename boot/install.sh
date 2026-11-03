#!/usr/bin/env bash
# Installs the boot service: a systemd user unit that runs up.sh after every boot, with
# lingering enabled so it starts without anyone logging in. Also makes Docker restart the
# kind nodes after a reboot (kind creates them with restart=on-failure, which a clean
# shutdown doesn't trigger). Safe to rerun. ./install.sh --remove undoes the unit.
set -euo pipefail
cd "$(dirname "$0")"
UNIT=kubecon-lab.service
DIR=$HOME/.config/systemd/user

if [ "${1:-}" = "--remove" ]; then
  systemctl --user disable --now "$UNIT" 2>/dev/null || true
  rm -f "$DIR/$UNIT"
  systemctl --user daemon-reload
  echo "Removed $UNIT (lingering and the kind restart policy are left as they are)."
  exit 0
fi

echo "==> kind nodes restart after a reboot"
d() { if docker info >/dev/null 2>&1; then docker "$@"; else sg docker -c "docker $(printf '%q ' "$@")"; fi; }
for c in kagent-control-plane kagent-worker kagent-worker2 kind-registry; do
  d update --restart=unless-stopped "$c" >/dev/null && echo "  $c: unless-stopped"
done

echo "==> systemd user unit $UNIT"
mkdir -p "$DIR"
sed "s|@UP@|$PWD/up.sh|" kubecon-lab.service > "$DIR/$UNIT"
systemctl --user daemon-reload
systemctl --user enable "$UNIT"

echo "==> start at boot without a login (linger)"
if [ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null)" != "yes" ]; then
  loginctl enable-linger "$USER" || { echo "Could not enable linger; run: sudo loginctl enable-linger $USER" >&2; exit 1; }
fi
echo "Linger: $(loginctl show-user "$USER" -p Linger --value)"
echo
echo "Installed. Run it now with: systemctl --user start $UNIT"
echo "Logs: journalctl --user -u $UNIT -f"
