#!/bin/bash
# 遠端工作台（dev-console）解除安裝：停 launchd 服務、刪 plist、移除 Claude hooks、移除本服務的 tailscale serve handler、刪安裝目錄。
# 只動本工具建立的東西：plist 只刪 <前綴>.dev-console.*，hooks 只刪帶 src=dev-console-hook 標記的那幾筆，
# tailscale serve 只移除「443 根路徑 / 指向本服務」的那一條 handler（同一個 443 上使用者的其他路徑原封不動），
# 其他設定（含同前綴的其他 plist、其他 hook）原封不動。
# 保留：ttyd／tmux（brew uninstall 自行移除）、aiuse（選用的另一個工具，不屬本 repo）、Tailscale ACL 規則（到 admin console 手動刪）。
# 可重跑：已解除過（找不到安裝紀錄也沒有安裝目錄）時印「看起來已解除」並以 0 結束。
#
# 參數（環境變數）：
#   TARGET_HOME       安裝目標家目錄（預設 ${HOME}）
#   DC_LABEL_PREFIX   安裝時用的前綴；沒給就讀 <TARGET_HOME>/.local/share/dev-console/install.env
#   NO_LOAD           1＝只刪檔：不執行 launchctl、不關 tmux session、不呼叫 tailscale；0＝一律呼叫。
#                     沒給時自動判斷：install.env 記錄為只產檔（DC_INSTALL_MODE=generate）→ 1，否則 0。
#                     TARGET_HOME 不是本帳號真正的家目錄（以 dscl 查，不看 ${HOME}）時一律強制 1，NO_LOAD=0 也不生效
#   DC_KEEP_LOGS=1    保留 <TARGET_HOME>/Library/Logs/dev-console（預設一併刪除）
set -euo pipefail
REPO_DIR="$(cd "$(dirname "$0")" && pwd -P)"
TARGET_HOME="${TARGET_HOME:-$HOME}"
UID_NUM="$(id -u)"
LA_DIR="$TARGET_HOME/Library/LaunchAgents"
PREFIX="$TARGET_HOME/.local/share/dev-console"
LOG_DIR="$TARGET_HOME/Library/Logs/dev-console"

# 安裝紀錄只拿需要的鍵，不 source（避免檔案被改過時執行任意指令）。
read_install_env() { [ -f "$PREFIX/install.env" ] && sed -n "s/^$1=//p" "$PREFIX/install.env" | head -1 || true; }
LABEL_PREFIX_BASE="${DC_LABEL_PREFIX:-$(read_install_env DC_LABEL_PREFIX)}"
if [ -z "$LABEL_PREFIX_BASE" ] && [ ! -e "$PREFIX" ]; then
  # 安裝目錄與 install.env 都不在：多半是已經解除過。重複解除不算錯，不要求使用者再提供前綴。
  echo "看起來已解除（找不到 ${PREFIX}）；沒有要做的事。若確定還有殘留的 plist，請在指令前加 DC_LABEL_PREFIX=<前綴> 重跑。"
  exit 0
fi
[[ "$LABEL_PREFIX_BASE" =~ ^[A-Za-z0-9][A-Za-z0-9-]*(\.[A-Za-z0-9][A-Za-z0-9-]*)+$ ]] || {
  echo "錯誤：${PREFIX} 還在但找不到安裝時的 DC_LABEL_PREFIX（install.env 不在或格式不對），請以環境變數提供" >&2
  exit 1
}
LABEL_PREFIX="$LABEL_PREFIX_BASE.dev-console"
UI_PORT="${DC_UI_PORT:-$(read_install_env DC_UI_PORT)}"
UI_PORT="${UI_PORT:-7690}"

# ---------- 真機保護 ----------
# 只有「目標家目錄＝這個帳號真正的家目錄」才可能碰 launchctl／tailscale／tmux（與 brew）。
# 真正的家目錄取自目錄服務（dscl），不信 ${HOME}：驗收常以 env -i HOME=<假目錄> 執行，${HOME} 會被換掉，
# 但 launchctl 的 gui/<uid> 網域、tailscale 與 tmux server 仍是真機的。不相同時一律強制只產檔／只刪檔。
real_home() {
  local h
  h="$(dscl . -read "/Users/$(id -un)" NFSHomeDirectory 2>/dev/null | sed -n 's/^NFSHomeDirectory: //p')"
  [ -n "$h" ] || h="$(eval "printf '%s' ~$(id -un)")"
  (cd "$h" 2>/dev/null && pwd -P) || printf '%s' "$h"
}
resolve_dir() { (cd "$1" 2>/dev/null && pwd -P) || printf '%s' "$1"; }
REAL_HOME="$(real_home)"
if [ "$(resolve_dir "$TARGET_HOME")" != "$REAL_HOME" ]; then
  if [ "${NO_LOAD:-}" != "1" ]; then
    echo "提醒：TARGET_HOME（${TARGET_HOME}）不是本帳號的家目錄（${REAL_HOME}），改為只處理檔案、不呼叫 launchctl／tailscale／tmux。" >&2
  fi
  NO_LOAD=1
fi

# 模式沒明確給就自動判斷：只產檔過的安裝沒有載入任何服務，不該去碰 launchctl／tailscale。
if [ -z "${NO_LOAD:-}" ]; then
  if [ "$(read_install_env DC_INSTALL_MODE)" = "generate" ]; then
    NO_LOAD=1
  else
    NO_LOAD=0
  fi
fi
[ "$NO_LOAD" = "1" ] && echo "只刪檔模式：不呼叫 launchctl／tailscale／tmux。"

for plist in "$LA_DIR/$LABEL_PREFIX".*.plist; do
  [ -e "$plist" ] || continue
  label="$(basename "$plist" .plist)"
  [ "$NO_LOAD" = "1" ] || launchctl bootout "gui/$UID_NUM/$label" 2>/dev/null || true
  rm -f "${plist:?}"
  echo "已移除 ${label}"
done

# rc-* 常駐 session 是本工具建立的（@dc_created=1），一併關掉；其他 tmux session 不動。
if [ "$NO_LOAD" != "1" ] && command -v tmux >/dev/null; then
  tmux list-sessions -F '#{session_name} #{@dc_created}' 2>/dev/null | while read -r name flag; do
    case "$name" in rc-*) [ "$flag" = "1" ] && tmux kill-session -t "=$name" && echo "已關閉 tmux session ${name}" ;; esac
  done || true
fi

TARGET_HOME="$TARGET_HOME" bash "$REPO_DIR/scripts/merge-hooks.sh" remove

# 只移除「https 443 的根路徑 / 指向本服務」這一條 handler（--set-path=/ off），不用 `--https=443 off`：
# 後者會關掉整個 443，連使用者掛在同一個 443 其他路徑上的服務一起消失。
if [ "$NO_LOAD" != "1" ] && command -v tailscale >/dev/null; then
  SERVE_JSON="$(tailscale serve status --json 2>/dev/null || echo '{}')"
  if jq -e --arg t "http://127.0.0.1:$UI_PORT" \
    '[.Web // {} | to_entries[] | select(.key | endswith(":443")) | .value.Handlers["/"].Proxy] | index($t) != null' \
    <<<"$SERVE_JSON" >/dev/null; then
    if tailscale serve --https=443 --set-path=/ off; then
      echo "已移除 tailscale serve https:443 / → http://127.0.0.1:${UI_PORT}（443 上其他路徑不動）"
    else
      echo "警告：移除 tailscale serve handler 失敗；請手動執行 tailscale serve --https=443 --set-path=/ off" >&2
    fi
  fi
fi

if [ -e "$PREFIX" ]; then
  rm -rf "${PREFIX:?}"
  echo "已刪除 ${PREFIX}"
fi
if [ "${DC_KEEP_LOGS:-0}" != "1" ] && [ -d "$LOG_DIR" ]; then
  rm -rf "${LOG_DIR:?}"
  echo "已刪除 ${LOG_DIR}"
fi
