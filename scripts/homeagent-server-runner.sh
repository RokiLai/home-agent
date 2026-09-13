#!/bin/sh
set -u

# HomeAgent Server 独立恢复监督脚本
# 由宿主机服务管理器（如 systemd）托管拉起，在服务端异常崩溃时自动执行二进制回滚自愈。

BIN_PATH="${HOMEAGENT_SERVER_BIN:-/usr/local/bin/homeagent-server}"
DATA_DIR="${HOMEAGENT_DATA_DIR:-/var/lib/homeagent}"
JOURNAL_FILE="${DATA_DIR}/server-upgrades.json"
BAK_PATH="${BIN_PATH}.bak"

# 导出受管环境变量，供服务端主进程环境自检
export HOMEAGENT_SUPERVISED="true"

# 启动主进程并透传参数
"$BIN_PATH" "$@"
EXIT_CODE=$?

# 若主进程非正常退出（崩溃、Panic 或依赖缺失）
if [ "$EXIT_CODE" -ne 0 ]; then
  echo "[supervisor] homeagent-server exited with code ${EXIT_CODE}" >&2
  if [ -f "$JOURNAL_FILE" ] && [ -f "$BAK_PATH" ]; then
    if grep -Eq '"status"[[:space:]]*:[[:space:]]*"(replacing|restarting|verifying)"' "$JOURNAL_FILE"; then
      echo "[supervisor] detected failed upgrade in progress, rolling back to ${BAK_PATH}..." >&2
      if cp -f "$BAK_PATH" "$BIN_PATH" && chmod 755 "$BIN_PATH"; then
        echo "[supervisor] rollback succeeded, backup binary restored" >&2
      else
        echo "[supervisor] rollback failed: could not restore backup binary" >&2
      fi
    fi
  fi
fi

exit "$EXIT_CODE"
