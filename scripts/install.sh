#!/bin/sh
set -eu

: "${HOMEAGENT_SERVER:?HOMEAGENT_SERVER is required}"
: "${HOMEAGENT_JOIN_TOKEN:?HOMEAGENT_JOIN_TOKEN is required}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  armv7*|armv6*|arm) arch=arm ;;
  mips|mips64) arch=mips ;;
  mipsel|mips64el|mipsle) arch=mipsle ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in darwin|linux) ;; *) echo "unsupported OS: $os" >&2; exit 1 ;; esac

tmp_file=$(mktemp "${TMPDIR:-/tmp}/homeagent-agent.XXXXXX" 2>/dev/null || echo "/tmp/homeagent-agent.$$")
trap 'rm -f "$tmp_file"' EXIT HUP INT TERM

url="${HOMEAGENT_SERVER%/}/downloads/homeagent-agent-${os}-${arch}"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$tmp_file"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$tmp_file" "$url"
else
  echo "curl or wget is required" >&2
  exit 1
fi

chmod 755 "$tmp_file"
install_dir=${HOMEAGENT_INSTALL_DIR:-}
if [ -z "$install_dir" ]; then
  if [ -d "/usr/local/bin" ] && [ -w "/usr/local/bin" ]; then
    install_dir="/usr/local/bin"
  elif [ -d "/usr/bin" ] && [ -w "/usr/bin" ]; then
    install_dir="/usr/bin"
  else
    install_dir="/usr/local/bin"
  fi
fi

if [ -w "$install_dir" ]; then
  cp "$tmp_file" "$install_dir/homeagent-agent"
  chmod 755 "$install_dir/homeagent-agent"
else
  sudo cp "$tmp_file" "$install_dir/homeagent-agent"
  sudo chmod 755 "$install_dir/homeagent-agent"
fi

# 1. Register device to server
"$install_dir/homeagent-agent" join --server "$HOMEAGENT_SERVER" --token "$HOMEAGENT_JOIN_TOKEN"

# 2. Configure and start background daemon service
"$install_dir/homeagent-agent" service install --server "$HOMEAGENT_SERVER" --token "$HOMEAGENT_JOIN_TOKEN" --binary "$install_dir/homeagent-agent"

echo "HomeAgent installation and daemon startup completed successfully!"
