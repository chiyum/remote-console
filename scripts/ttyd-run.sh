#!/bin/bash
# launchd 用的 ttyd 啟動器：清掉上次被 kill -9 殘留的 unix socket，再 exec ttyd。
# ttyd 只開 unix socket（不開 TCP port），只有同一使用者的 dev-console 能連。
# disableReconnect：ttyd 前端斷線後會「立刻」重連、沒有退避，ttyd 掛掉時每秒打好幾次；
# 改由 dev-console 注入的 termguard.js 查健康狀態、退避後重新載入頁面（ADR 0006）。
set -eu
SOCK="${DC_TTYD_SOCKET:?}"
RUN_DIR="$(dirname "$SOCK")"
mkdir -p "$RUN_DIR"
chmod 700 "$RUN_DIR"
rm -f "${SOCK:?}"
exec "${DC_TTYD_BIN:-/opt/homebrew/bin/ttyd}" \
  -i "$SOCK" \
  -W -a \
  -b /term \
  -P 10 \
  -t titleFixed=dev-console \
  -t disableLeaveAlert=true \
  -t disableReconnect=true \
  -t fontSize=14 \
  "${DC_BIN_DIR:?}/ttyd-attach.sh"
