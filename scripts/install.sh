#!/bin/sh
set -eu

: "${HOMEAGENT_SERVER:?HOMEAGENT_SERVER is required}"
: "${HOMEAGENT_JOIN_TOKEN:?HOMEAGENT_JOIN_TOKEN is required}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in darwin|linux) ;; *) echo "unsupported OS: $os" >&2; exit 1 ;; esac

tmp_file=$(mktemp "${TMPDIR:-/tmp}/homeagent-agent.XXXXXX")
trap 'rm -f "$tmp_file"' EXIT HUP INT TERM
curl -fsSL "${HOMEAGENT_SERVER%/}/downloads/homeagent-agent-${os}-${arch}" -o "$tmp_file"
chmod 755 "$tmp_file"
install_dir=${HOMEAGENT_INSTALL_DIR:-/usr/local/bin}
if [ -w "$install_dir" ]; then
  install -m 755 "$tmp_file" "$install_dir/homeagent-agent"
else
  sudo install -m 755 "$tmp_file" "$install_dir/homeagent-agent"
fi
"$install_dir/homeagent-agent" join --server "$HOMEAGENT_SERVER" --token "$HOMEAGENT_JOIN_TOKEN"
