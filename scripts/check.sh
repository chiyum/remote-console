#!/bin/bash
# 本 repo 的確定性檢查：Go 格式／vet／測試＋shell 語法＋「$變數緊接非 ASCII 字元」，最後一列是本機 ttyd 健康狀態。
# 用法：scripts/check.sh（全部）；scripts/check.sh ttyd（只看 ttyd 狀態）。ttyd 異常時結束碼非 0。
set -euo pipefail
cd "$(dirname "$0")/.."

# ttyd 狀態（ADR 0006）：用已安裝的 dev-console 執行檔查，判斷邏輯與服務內的自動恢復同一套。
# 門檻與上限從 web 服務的 plist 讀，與服務實際使用的值一致；本機沒安裝就略過（例如在別台只跑程式檢查）。
ttyd_status() {
  local bin="$HOME/.local/share/dev-console/bin/dev-console"
  # label 前綴由安裝者決定，從安裝紀錄讀回；沒有紀錄就視為未安裝。
  local prefix
  # install.env 不存在時 sed 失敗；set -e／pipefail 下要明確吞掉，否則整支腳本會無聲以 1 結束。
  prefix="$(sed -n 's/^DC_LABEL_PREFIX=//p' "$HOME/.local/share/dev-console/install.env" 2>/dev/null | head -1 || true)"
  local plist="$HOME/Library/LaunchAgents/${prefix:-none}.dev-console.web.plist"
  if [ ! -x "$bin" ] || [ ! -f "$plist" ]; then
    echo "ttyd：略過（本機未安裝 dev-console）"
    return 0
  fi
  local k v
  for k in DC_TTYD_SOCKET DC_TTYD_LABEL DC_TTYD_NOFILE DC_TTYD_FD_RESTART; do
    if v="$(plutil -extract "EnvironmentVariables.$k" raw "$plist" 2>/dev/null)"; then
      export "$k=$v"
    fi
  done
  "$bin" ttyd-status
}
if [ "${1:-}" = "ttyd" ]; then
  ttyd_status
  exit $?
fi
fmt="$(gofmt -l .)"
[ -z "$fmt" ] || { echo "gofmt 未通過：$fmt" >&2; exit 1; }
go vet ./...
go test ./...
for f in install.sh uninstall.sh scripts/*.sh; do bash -n "$f"; done
# UTF-8 locale 下 bash 3.2 會把 $var 後面的中文併進變數名（set -u 時爆 unbound variable）。
scripts/check-shell-vars.sh
echo "check OK"
ttyd_status
