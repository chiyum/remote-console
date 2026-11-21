#!/bin/bash
# 把 dev-console 的 Claude hooks 冪等地合併進 Claude 的 settings.json：
#   預設設定 <TARGET_HOME>/.claude/settings.json，以及每個 profile 的 <TARGET_HOME>/.ai-profiles/claude/<profile>/settings.json。
#
# 用法：TARGET_HOME=<家目錄，預設 $HOME> merge-hooks.sh install|remove [hook_port]
#   install：先移除舊的 dev-console hook（以 src=dev-console-hook 標記辨識），再加上新的；
#            其他 hook（例如通知類 hook）原封不動。內容沒變就不寫檔。
#   remove ：只移除 dev-console hook；檔案若因此回到空物件且原本是本腳本建立的，仍保留為 {}（不刪使用者檔案）。
# settings.json 或 profile 目錄本身是 symlink 時略過並警告：多半指向共用檔、repo 內的檔或目標 HOME 以外，改它會影響所有 profile 或寫穿出去。
#
# 寫檔用「同目錄 tmp + mv」原子替換：其他 profile 可能正有 claude 在跑並讀這個檔，
# 不能讓它讀到寫一半的 JSON。
set -euo pipefail
MODE="${1:-install}"
PORT="${2:-7691}"
TARGET_HOME="${TARGET_HOME:-$HOME}"
PROFILES_ROOT="${DC_PROFILES_ROOT:-$TARGET_HOME/.ai-profiles}/claude"
MARKER="src=dev-console-hook"
# PermissionRequest：權限對話框一出現就觸發（Notification(permission_prompt) 實測晚約 6 秒，只留作備援）。
EVENTS='["UserPromptSubmit","PreToolUse","PermissionRequest","PostToolUse","Notification","Stop","SessionEnd"]'

command -v jq >/dev/null || { echo "需要 jq" >&2; exit 1; }

# merge_one <settings 檔所在目錄> <profile 名稱，預設設定為空字串> <顯示名稱>
merge_one() {
  local dir="$1" profile="$2" name="$3" f src tmp cmd query
  f="$dir/settings.json"
  if [ -L "$f" ]; then
    echo "略過 ${name}：settings.json 是 symlink（避免改到共用檔）" >&2
    return 0
  fi
  [ -d "$dir" ] || return 0
  if [ -f "$f" ]; then
    src="$f"
  else
    [ "$MODE" = "remove" ] && return 0
    src=/dev/null
  fi
  # 指令說明：stdin 是 Claude 給的 hook JSON；TMUX_PANE 由 claude 的環境繼承，用來對回 tmux session。
  # 連線 0.5 秒、總長 1 秒逾時，失敗一律吞掉且不輸出任何東西（UserPromptSubmit 的 stdout 會進上下文）。
  # X-DC-Hook-Token 由 curl -H @<0600 header 檔> 讀入：容器等其他本機程序連得到 127.0.0.1 但讀不到這個檔；
  # 用檔案而非 $(cat …) 展開進命令列，token 才不會出現在 ps 看得到的參數裡。
  # 預設設定（非 profile）不帶 profile 參數；Go 端收到空 profile 視為預設帳號。
  query="${MARKER}"
  [ -n "$profile" ] && query="profile=${profile}&${MARKER}"
  cmd="curl -s -o /dev/null --connect-timeout 0.5 -m 1 -X POST -H 'Content-Type: application/json' -H \"X-Tmux-Pane: \${TMUX_PANE:-}\" -H @\"\$HOME/.local/share/dev-console/hook-header\" --data-binary @- 'http://127.0.0.1:${PORT}/api/hook?${query}' >/dev/null 2>&1 || true"
  tmp="$(mktemp "$dir/.settings.json.dc-XXXXXX")"
  if ! jq --arg cmd "$cmd" --arg marker "$MARKER" --arg mode "$MODE" --argjson events "$EVENTS" '
      (if type == "object" then . else {} end) as $root
      | $root
      | .hooks = (.hooks // {})
      # 1) 所有事件先拿掉 dev-console 的 hook；某個 matcher 因此變空就整筆移除。
      | .hooks |= with_entries(
          .value |= ( map(.hooks = ((.hooks // []) | map(select(((.command // "") | contains($marker)) | not))))
                      | map(select((.hooks | length) > 0)) ))
      # 2) install 模式再把新的加到每個事件尾端（排在既有 hook 之後，不影響它們）。
      | if $mode == "install" then
          reduce $events[] as $ev (.;
            .hooks[$ev] = ((.hooks[$ev] // []) + [{"matcher": "", "hooks": [{"type": "command", "command": $cmd, "timeout": 3}]}]))
        else . end
      # 3) 清掉空事件與空 hooks 物件，remove 後檔案回到原狀。
      | .hooks |= with_entries(select((.value | length) > 0))
      | if (.hooks | length) == 0 then del(.hooks) else . end
    ' <(if [ "$src" = /dev/null ]; then echo '{}'; else cat "$src"; fi) > "$tmp"; then
    rm -f "${tmp:?}"
    echo "錯誤：${name} settings.json 解析失敗，未修改" >&2
    exit 1
  fi
  if [ -f "$f" ] && cmp -s <(jq -S . "$f") <(jq -S . "$tmp"); then
    rm -f "${tmp:?}"
    echo "${name}：hooks 已是最新，未修改"
    return 0
  fi
  if [ ! -f "$f" ] && [ "$(jq -c . "$tmp")" = "{}" ]; then
    rm -f "${tmp:?}"
    return 0
  fi
  [ -f "$f" ] && chmod "$(stat -f %Lp "$f")" "$tmp" || chmod 644 "$tmp"
  mv -f "$tmp" "$f"
  if [ "$MODE" = "install" ]; then echo "${name}：hooks 已更新"; else echo "${name}：hooks 已移除"; fi
}

# 預設設定目錄還不存在（例如還沒開過 claude 的新機器）時，install 先建立它：
# 直接模式（沒有 aiuse）的燈號只靠這份設定；profile 目錄則不代建（不存在就不是 profile）。
if [ "$MODE" = "install" ] && [ ! -e "$TARGET_HOME/.claude" ]; then
  mkdir -p "$TARGET_HOME/.claude"
fi
merge_one "$TARGET_HOME/.claude" "" "預設（~/.claude）"
for dir in "$PROFILES_ROOT"/*/; do
  [ -d "$dir" ] || continue
  # "$PROFILES_ROOT"/*/ 會連「指向目錄的 symlink」一起列出；profile 目錄若是 symlink，
  # 改它的 settings.json 等於寫到 symlink 指向的位置（可能在目標 HOME 以外）。
  # 與 aiuse 的 aiuse_profile_dir_exact 同一規則：symlink 不算 profile，略過並說明。
  if [ -L "${dir%/}" ]; then
    echo "略過 $(basename "$dir")：profile 目錄是 symlink（避免寫到 symlink 指向的位置）" >&2
    continue
  fi
  profile="$(basename "$dir")"
  # 與 aiuse 相同的 profile 名稱規則（英數、底線、連字號，首字英數）；shared 是共用設定不是帳號。
  # 規則若比 aiuse 窄，aiuse 允許建立的 profile（例如 work_2、Work）會靜默沒有燈號。
  [[ "$profile" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || continue
  [ "$profile" = "shared" ] && continue
  merge_one "${dir%/}" "$profile" "$profile"
done
