#!/bin/sh
set -eu

: "${HOMEAGENT_SERVER:=https://homeagent.rokilai.online}"
token="${HOMEAGENT_CLAIM_TOKEN:-${HOMEAGENT_JOIN_TOKEN:-}}"
repo="${HOMEAGENT_GITHUB_REPO:-RokiLai/home-agent}"
version="${HOMEAGENT_AGENT_VERSION:-}"
valid_version() {
  printf '%s\n' "$1" | awk '/^(agent-)?v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/ { ok=1 } END { exit !(ok && NR==1) }'
}
if [ -z "${HOMEAGENT_DOWNLOAD_BASE_URL:-}" ] && [ -n "$version" ] && ! valid_version "$version"; then
  echo "invalid Agent release tag" >&2
  exit 1
fi
case "$repo" in
  *[!A-Za-z0-9_./-]*|../*|*/../*|*/..|/*) echo "invalid GitHub repository" >&2; exit 1 ;;
esac
if ! printf '%s\n' "$repo" | awk '/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/ { ok=1 } END { exit !ok }'; then
  echo "invalid GitHub repository" >&2; exit 1
fi
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
asset="homeagent-agent-${os}-${arch}"
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/homeagent-install.XXXXXX")
tmp_file="$tmp_dir/agent"
tmp_sha="$tmp_dir/checksum"
cleanup() {
  if [ -n "${transfer:-}" ]; then kill "$transfer" 2>/dev/null || :; wait "$transfer" 2>/dev/null || :; fi
  if [ -n "${watchdog:-}" ]; then kill "$watchdog" 2>/dev/null || :; wait "$watchdog" 2>/dev/null || :; fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# Both downloader paths have a wall-clock deadline, including wget retries.
fetch() {
  case "$1" in http://*|https://*) ;; *) echo "invalid download URL" >&2; return 1 ;; esac
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 10 --max-time 120 --proto '=http,https' --proto-redir '=http,https' \
      -A HomeAgent-Installer -D "$tmp_dir/headers" "$1" -o "$2" 2>"$tmp_dir/network-error" &
  elif command -v wget >/dev/null 2>&1; then
    wget -T 10 -U HomeAgent-Installer -S -O "$2" "$1" 2>"$tmp_dir/headers" &
  else
    echo "curl or wget is required" >&2; return 1
  fi
  transfer=$!
  (
    sleep 120 & timer=$!
    trap 'kill "$timer" 2>/dev/null || :; wait "$timer" 2>/dev/null || :; exit' TERM INT
    wait "$timer" || exit
    kill -TERM "$transfer" 2>/dev/null || :
  ) &
  watchdog=$!
  result=0
  wait "$transfer" || result=$?
  kill "$watchdog" 2>/dev/null || :
  wait "$watchdog" 2>/dev/null || :
  transfer=
  watchdog=
  if [ "$result" -ne 0 ]; then
    echo "HTTP download failed (network, timeout or non-2xx response)" >&2
    return "$result"
  fi
}

# A recursive JSON parser: object identity and decoded keys matter. Release
# descriptions and nested author objects must never become release records.
parse_page() {
  LC_ALL=C awk -v asset="$asset" '
  function fail() { print "invalid GitHub release response" > "/dev/stderr"; exit 1 }
  function ws() { while (substr(s,p,1) ~ /[ \t\r\n]/ && p<=length(s)) p++ }
  function hex(h, i,n,x) { n=0; for(i=1;i<=length(h);i++){x=index("0123456789abcdef",tolower(substr(h,i,1)))-1;if(x<0)fail();n=n*16+x} return n }
  function utf8(c, n,need,min,code,i,b) {
    n=byte[c]
    if(n>=194 && n<=223){need=1;min=128;code=n-192}
    else if(n>=224 && n<=239){need=2;min=2048;code=n-224}
    else if(n>=240 && n<=244){need=3;min=65536;code=n-240}
    else fail()
    for(i=1;i<=need;i++) {
      b=byte[substr(s,p,1)];if(b<128 || b>191)fail()
      c=c substr(s,p++,1);code=code*64+b-128
    }
    if(code<min || code>1114111 || (code>=55296 && code<=57343))fail()
    return c
  }
  function str( c,out,e,h,n) {
    if(substr(s,p++,1)!="\"")fail()
    out=""
    while(p<=length(s)) {
      c=substr(s,p++,1)
      if(c=="\"")return out
      if(c ~ /[[:cntrl:]]/)fail()
      if(byte[c]>=128)c=utf8(c)
      if(c=="\\") {
        e=substr(s,p++,1)
        if(e=="u") {
          h=substr(s,p,4);if(length(h)!=4)fail();n=hex(h);p+=4
          if(n<128)out=out sprintf("%c",n);else out=out "\\u" tolower(h)
        } else if(e=="\"" || e=="\\" || e=="/")out=out e
        else if(e=="b")out=out sprintf("%c",8)
        else if(e=="f")out=out sprintf("%c",12)
        else if(e=="n")out=out "\n"
        else if(e=="r")out=out "\r"
        else if(e=="t")out=out "\t"
        else fail()
      } else out=out c
    }
    fail()
  }
  function value(depth, id,c,key,k,v,start) {
    if(depth>128)fail()
    ws();id=++nodes;c=substr(s,p,1)
    if(c=="{" || c=="[") {
      typ[id]=c;p++;ws()
      if(substr(s,p,1)==(c=="{"?"}":"]")){p++;return id}
      do {
        ws()
        if(c=="{"){key=str();ws();if(substr(s,p++,1)!=":")fail()}
        else key=++count[id]
        k=id SUBSEP key
        if(k in child)fail()
        v=value(depth+1);child[k]=v;ws()
        if(substr(s,p,1)!=",")break
        p++
      } while(1)
      if(substr(s,p++,1)!=(c=="{"?"}":"]"))fail()
    } else if(c=="\""){typ[id]="string";val[id]=str()}
    else if(substr(s,p,4)=="true"){typ[id]="bool";val[id]="true";p+=4}
    else if(substr(s,p,5)=="false"){typ[id]="bool";val[id]="false";p+=5}
    else if(substr(s,p,4)=="null"){typ[id]="null";p+=4}
    else {
      if(!match(substr(s,p),/^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?/))fail()
      typ[id]="number";p+=RLENGTH
    }
    return id
  }
  function field(id,key,type, n){n=child[id SUBSEP key];if(!n || typ[n]!=type)fail();return n}
  BEGIN { for(b=1;b<=255;b++)byte[sprintf("%c",b)]=b }
  { s=s $0 "\n" }
  END {
    p=1;root=value(0);ws();if(p<=length(s) || typ[root]!="[")fail()
    for(i=1;i<=count[root];i++) {
      r=child[root SUBSEP i];if(typ[r]!="{")fail()
      tag=val[field(r,"tag_name","string")]
      draft=val[field(r,"draft","bool")];pre=val[field(r,"prerelease","bool")]
      a=field(r,"assets","[");binary=0;sha=0
      for(j=1;j<=count[a];j++) {
        item=child[a SUBSEP j];if(typ[item]!="{")fail()
        name=val[field(item,"name","string")]
        if(name==asset)binary++
        if(name==asset ".sha256")sha++
      }
      if(draft=="false" && pre=="false" && tag ~ /^agent-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/)print tag,binary,sha
    }
    print "COUNT",count[root]+0
  }' "$tmp_dir/page"
}

if [ -n "${HOMEAGENT_DOWNLOAD_BASE_URL:-}" ]; then
  url="${HOMEAGENT_DOWNLOAD_BASE_URL%/}/$asset"
else
  if [ -z "$version" ]; then
    page=1
    : >"$tmp_dir/candidates"
    while :; do
      fetch "https://api.github.com/repos/${repo}/releases?per_page=100&page=${page}" "$tmp_dir/page"
      parse_page >"$tmp_dir/parsed"
      cat "$tmp_dir/parsed" >>"$tmp_dir/candidates"
      count=$(awk '$1=="COUNT" {print $2}' "$tmp_dir/parsed")
      if [ "$count" -lt 100 ] && ! awk 'tolower($0) ~ /^[ \t]*link:/ && tolower($0) ~ /rel="?next"?/ {found=1} END {exit !found}' "$tmp_dir/headers"; then break; fi
      if [ "$page" -eq 10 ]; then echo "incomplete GitHub release scan" >&2; exit 1; fi
      page=$((page+1))
    done
    version=$(awk '
      function newer(a,b, x,y,i){sub(/^agent-v/,"",a);sub(/^agent-v/,"",b);split(a,x,".");split(b,y,".");for(i=1;i<=3;i++){if(length(x[i])!=length(y[i]))return length(x[i])>length(y[i]);if("x" x[i]!="x" y[i])return "x" x[i]>"x" y[i]}return 0}
      $1!="COUNT" {if(!best || newer($1,best)){best=$1;binary=$2;sha=$3}else if($1==best && (binary!=$2 || sha!=$3))bad=1}
      END {if(!best){print "no stable Agent release" > "/dev/stderr";exit 1}if(binary!=1 || sha!=1 || bad){print "Agent release assets missing or duplicated" > "/dev/stderr";exit 1}print best}
    ' "$tmp_dir/candidates")
  fi
  url="https://github.com/${repo}/releases/download/${version}/$asset"
fi
sha_url="${url}.sha256"
fetch "$url" "$tmp_file"
fetch "$sha_url" "$tmp_sha"
expected_sha=$(awk -v asset="$asset" '
  NF {n++; if(NF>2 || length($1)!=64 || $1 ~ /[^0-9a-fA-F]/ || (NF==2 && $2!=asset && $2!="*" asset))bad=1;digest=tolower($1)}
  END {if(n!=1 || NR!=1 || bad){print "invalid SHA256 checksum file" > "/dev/stderr";exit 1}print digest}
' "$tmp_sha")

actual_sha=""
if command -v sha256sum >/dev/null 2>&1; then
  actual_sha=$(sha256sum "$tmp_file" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
elif command -v shasum >/dev/null 2>&1; then
  actual_sha=$(shasum -a 256 "$tmp_file" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
elif command -v openssl >/dev/null 2>&1; then
  actual_sha=$(openssl dgst -sha256 "$tmp_file" | awk '{print $NF}' | tr '[:upper:]' '[:lower:]')
else
  echo "sha256sum, shasum, or openssl is required for checksum verification" >&2
  exit 1
fi

if [ "$expected_sha" != "$actual_sha" ]; then
  echo "SHA256 checksum mismatch for $url" >&2
  echo "Expected: $expected_sha" >&2
  echo "Actual:   $actual_sha" >&2
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
  info_plist_tmp="$tmp_dir/Info.plist"
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

if { [ -d "$install_dir" ] && [ -w "$install_dir" ]; } || { [ ! -e "$install_dir" ] && [ -w "$(dirname "$install_dir")" ]; }; then
  mkdir -p "$install_dir"
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

if [ -n "$token" ]; then
  # 1. Claim device and persist dedicated Device Token locally (0600 mode)
  "$install_dir/homeagent-agent" claim --server "$HOMEAGENT_SERVER" --claim-token "$token"

  # 2. Configure and start background daemon service using persisted device configuration
  "$install_dir/homeagent-agent" service install --server "$HOMEAGENT_SERVER" --binary "$install_dir/homeagent-agent"

  echo "HomeAgent onboarding and daemon startup completed successfully!"
else
  echo ""
  echo "HomeAgent Agent binary installed successfully to: $install_dir/homeagent-agent"
  echo "To complete device onboarding, run:"
  echo "  $install_dir/homeagent-agent claim --server \"$HOMEAGENT_SERVER\" --claim-token \"<YOUR_CLAIM_TOKEN>\""
  echo "  $install_dir/homeagent-agent service install --binary \"$install_dir/homeagent-agent\""
  echo ""
fi
