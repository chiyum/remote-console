#!/bin/bash
# Remote Control 常駐器（選用，預設不裝；install.sh 只在設了 DC_RC_PROFILE 時才裝，launchd KeepAlive 跑本腳本）：
#   - tmux session 不存在 → 重建：有 aiuse 時執行 aiuse claude <profile> remote-control；
#     沒有 aiuse（直接模式）時以預設帳號執行 claude remote-control，DC_RC_PROFILE 只用來命名。
#     每次重建都重新判斷，與網頁新開 session 的模式判斷一致。
#   - 本腳本自己不會因 claude／aiuse 不在而結束（只記 log、照冷卻重試），launchd 不會陷入快速重啟迴圈。
#   - pane 已結束（remain-on-exit 保留畫面供除錯）→ 死掉滿 60 秒才重起，避免登入失效時狂重啟。
#   - 家目錄的「Trust <家目錄>? [y/N]」每次啟動都會問（家目錄信任不會被記住），
#     偵測到這行就自動回 y；這等同使用者平常在家目錄開 claude 時按的確認。DC_RC_AUTO_TRUST=0 可關。
set -u
TMUX_BIN="${DC_TMUX_PATH:-/opt/homebrew/bin/tmux}"
AIUSE="${DC_AIUSE_PATH:-$HOME/.local/bin/aiuse}"
# profile 由安裝者指定（install.sh 的 DC_RC_PROFILE），沒有預設值：不該替使用者猜要保活哪個帳號。
PROFILE="${DC_RC_PROFILE:?DC_RC_PROFILE 未設定（remote-control 常駐為選用功能）}"
SESSION="${DC_RC_SESSION:-rc-$PROFILE}"
SPAWN="${DC_RC_SPAWN:-same-dir}"
AUTO_TRUST="${DC_RC_AUTO_TRUST:-1}"
INTERVAL="${DC_RC_INTERVAL:-10}"
COOLDOWN="${DC_RC_RESTART_COOLDOWN:-60}"

log() { printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }

auto_trust() {
  [ "$AUTO_TRUST" = "1" ] || return 0
  local i screen
  for i in $(seq 1 30); do
    sleep 1
    screen="$("$TMUX_BIN" capture-pane -p -J -t "=$SESSION:" 2>/dev/null || true)"
    if printf '%s' "$screen" | grep -qF "Trust $HOME? [y/N]"; then
      "$TMUX_BIN" send-keys -t "=$SESSION:" y Enter
      log "已回覆家目錄信任提示"
      return 0
    fi
    if printf '%s' "$screen" | grep -qE 'Connected|Error'; then
      return 0
    fi
  done
}

# claude_bin：直接模式要執行的 claude（DC_CLAUDE_BIN 優先，否則從 PATH 找；PATH 由 plist 給，與網頁新開 session 相同）。
claude_bin() {
  if [ -n "${DC_CLAUDE_BIN:-}" ]; then
    [ -f "$DC_CLAUDE_BIN" ] && [ -x "$DC_CLAUDE_BIN" ] && printf '%s' "$DC_CLAUDE_BIN"
    return 0
  fi
  command -v claude 2>/dev/null || true
}

start_session() {
  local argv
  if [ -f "$AIUSE" ] && [ -x "$AIUSE" ]; then
    argv=("$AIUSE" claude "$PROFILE" remote-control --spawn "$SPAWN" --name "$SESSION")
  else
    local claude
    claude="$(claude_bin)"
    if [ -z "$claude" ]; then
      log "直接模式找不到 claude（PATH=${PATH}）；${COOLDOWN} 秒後再試"
      return 1
    fi
    argv=("$claude" remote-control --spawn "$SPAWN" --name "$SESSION")
  fi
  log "建立 ${SESSION}（${argv[*]}）"
  # 選項與建 session 放在同一次 tmux 呼叫（\; 串接，server 依序執行完才處理子程序結束）：
  # 若分開下，remote-control 立刻失敗時 pane 會在 remain-on-exit 生效前就消失，錯誤畫面也跟著丟。
  # @aiuse_profile 只在 aiuse 模式寫：直接模式沒有 profile，不冒充 aiuse 管理的 session。
  local tag=(set-option -q -t "=$SESSION:" @dc_created 1)
  if [ "${argv[0]}" = "$AIUSE" ]; then
    tag=(set-option -q -t "=$SESSION:" @aiuse_profile "$PROFILE" \; set-option -q -t "=$SESSION:" @aiuse_tool claude \; "${tag[@]}")
  fi
  "$TMUX_BIN" new-session -d -s "$SESSION" -c "$HOME" \
    -e "PATH=$PATH" -e "LANG=${LANG:-en_US.UTF-8}" -- \
    "${argv[@]}" \; \
    set-option -q -t "=$SESSION:" remain-on-exit on \; \
    "${tag[@]}" || return 1
  auto_trust
}

dead_since=0
while true; do
  if ! "$TMUX_BIN" has-session -t "=$SESSION" 2>/dev/null; then
    dead_since=0
    # 建立失敗（含找不到 claude）時多等一個冷卻再試，不要每 INTERVAL 秒就重試一次。
    start_session || { log "建立失敗"; sleep "$COOLDOWN"; }
  else
    dead="$("$TMUX_BIN" display-message -p -t "=$SESSION:" '#{pane_dead}' 2>/dev/null || echo 0)"
    if [ "$dead" = "1" ]; then
      now=$(date +%s)
      if [ "$dead_since" -eq 0 ]; then
        dead_since=$now
        log "remote-control 已結束，${COOLDOWN} 秒後重起（畫面保留在 ${SESSION}）"
      elif [ $((now - dead_since)) -ge "$COOLDOWN" ]; then
        dead_since=0
        log "重起 remote-control"
        "$TMUX_BIN" respawn-pane -k -t "=$SESSION:" && auto_trust
      fi
    else
      dead_since=0
    fi
  fi
  sleep "$INTERVAL"
done
