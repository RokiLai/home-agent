#!/bin/bash
set -euo pipefail

# build-macos-archive-v2.sh
# 打包并生成符合 macos-app-archive-v2 规范的 ZIP 归档与 Ed25519 签名的 manifest.json

APP_DIR="${1:-}"
OUT_DIR="${2:-dist}"
VERSION="${3:-}"
KEY_FILES="${4:-}"
KEY_IDS="${5:-}"

if [[ -z "$APP_DIR" || -z "$VERSION" ]]; then
    echo "Usage: $0 <path/to/HomeAgent.app> <output_dir> <target_version> [key_files_csv] [key_ids_csv]"
    echo "Example: $0 ./HomeAgent.app ./dist v0.7.0 keys/signer1.hex,keys/signer2.hex key1,key2"
    exit 1
fi

mkdir -p "$OUT_DIR"
ZIP_PATH="${OUT_DIR}/homeagent-app-macos-arm64-${VERSION}.zip"
MANIFEST_PATH="${OUT_DIR}/manifest.json"

echo "Building homeagent-archive builder..."
go build -o "${OUT_DIR}/homeagent-archive" ./cmd/homeagent-archive

PACK_CMD=("${OUT_DIR}/homeagent-archive" "pack" "-app" "$APP_DIR" "-out" "$ZIP_PATH" "-version" "$VERSION" "-manifest-out" "$MANIFEST_PATH")

if [[ -n "$KEY_FILES" && -n "$KEY_IDS" ]]; then
    PACK_CMD+=("-keys" "$KEY_FILES" "-key-ids" "$KEY_IDS")
fi

echo "Packaging $APP_DIR into $ZIP_PATH..."
"${PACK_CMD[@]}"

echo "Packaging complete:"
echo "  Archive:  $ZIP_PATH"
echo "  Manifest: $MANIFEST_PATH"
