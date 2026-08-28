#!/usr/bin/env sh
# HomeAgent Helper Script for ddns-go / External DDNS tools
# Usage: ./get-device-ipv6.sh <device_id> [server_url] [token]

set -e

DEVICE_ID="${1}"
if [ -z "${DEVICE_ID}" ]; then
  echo "Error: device ID is required" >&2
  echo "Usage: $0 <device_id> [server_url] [token]" >&2
  exit 1
fi

SERVER_URL="${2:-${HOMEAGENT_SERVER:-http://127.0.0.1:8080}}"
SERVER_URL="${SERVER_URL%/}"
TOKEN="${3:-${HOMEAGENT_JOIN_TOKEN}}"

if [ -z "${TOKEN}" ]; then
  echo "Error: token is required via argument or HOMEAGENT_JOIN_TOKEN" >&2
  exit 1
fi

# Fetch IPv6 plain text directly from HomeAgent Server
RESPONSE=$(curl -fsS -H "Authorization: Bearer ${TOKEN}" "${SERVER_URL}/api/v1/devices/${DEVICE_ID}/ipv6" 2>/dev/null)

if [ -z "${RESPONSE}" ]; then
  echo "Error: no valid IPv6 address received for device ${DEVICE_ID}" >&2
  exit 1
fi

# Output IPv6 address to stdout for ddns-go / external consumers
echo "${RESPONSE}"
