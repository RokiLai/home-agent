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
  if [ "$os" = "darwin" ]; then
    install_dir="/Applications/HomeAgent.app/Contents/MacOS"
  elif [ -d "/usr/local/bin" ] && [ -w "/usr/local/bin" ]; then
    install_dir="/usr/local/bin"
  elif [ -d "/usr/bin" ] && [ -w "/usr/bin" ]; then
    install_dir="/usr/bin"
  else
    install_dir="/usr/local/bin"
  fi
fi

if [ "$os" = "darwin" ]; then
  case "$install_dir" in
    *.app/Contents/MacOS) ;;
    *) echo "on macOS, HOMEAGENT_INSTALL_DIR must end with .app/Contents/MacOS" >&2; exit 1 ;;
  esac
fi

if [ "$os" = "darwin" ]; then
  app_root=$(dirname "$(dirname "$install_dir")")
  info_plist_tmp=$(mktemp "${TMPDIR:-/tmp}/homeagent-info.XXXXXX")
  trap 'rm -f "$tmp_file" "$info_plist_tmp"' EXIT HUP INT TERM
  cat >"$info_plist_tmp" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>en</string>
  <key>CFBundleExecutable</key>
  <string>homeagent-agent</string>
  <key>CFBundleIdentifier</key>
  <string>com.homeagent.app</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>HomeAgent</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleVersion</key>
  <string>1</string>
  <key>NSLocalNetworkUsageDescription</key>
  <string>HomeAgent needs to connect to the HomeAgent server on your local network.</string>
</dict>
</plist>
EOF
  if [ -d "$app_root" ] && [ ! -w "$app_root" ]; then
    sudo mkdir -p "$install_dir"
    sudo cp "$info_plist_tmp" "$app_root/Contents/Info.plist"
  elif [ ! -w "$(dirname "$app_root")" ]; then
    sudo mkdir -p "$install_dir"
    sudo cp "$info_plist_tmp" "$app_root/Contents/Info.plist"
  else
    mkdir -p "$install_dir"
    cp "$info_plist_tmp" "$app_root/Contents/Info.plist"
  fi
fi

if [ -d "$install_dir" ] && [ -w "$install_dir" ]; then
  cp "$tmp_file" "$install_dir/homeagent-agent"
  chmod 755 "$install_dir/homeagent-agent"
else
  sudo mkdir -p "$install_dir"
  sudo cp "$tmp_file" "$install_dir/homeagent-agent"
  sudo chmod 755 "$install_dir/homeagent-agent"
fi

if [ "$os" = "darwin" ]; then
  if ! codesign --verify --deep --strict "$app_root" >/dev/null 2>&1; then
    echo "warning: HomeAgent.app is not signed with a verifiable release identity; Local Network permission may need to be granted again after upgrades" >&2
  fi
fi

# 1. Register device to server
"$install_dir/homeagent-agent" join --server "$HOMEAGENT_SERVER" --token "$HOMEAGENT_JOIN_TOKEN"

# 2. Configure and start background daemon service
"$install_dir/homeagent-agent" service install --server "$HOMEAGENT_SERVER" --token "$HOMEAGENT_JOIN_TOKEN" --binary "$install_dir/homeagent-agent"

echo "HomeAgent installation and daemon startup completed successfully!"
