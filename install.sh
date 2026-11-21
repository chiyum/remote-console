#!/bin/bash
# 遠端工作台（remote-console，服務名 dev-console）安裝腳本：冪等，可重跑；步驟與三個設定題見 README「安裝」。只支援 macOS。
#
# 做的事：檢查依賴 → go build → 判斷模式（有沒有 aiuse）→ 安裝 binary 與輔助腳本 → 產生 launchd plist（內容相同不重寫）並載入
#        → 寫 install.env → 合併 Claude hooks → 檢查 tmux window-size → 設定 tailscale serve（已設就跳過）。
# 不做的事：不改系統能源設定（只提示指令）、不動 Tailscale ACL（另跑 scripts/tailscale-acl.sh）、不開 funnel、
#          不安裝 aiuse（選用的多帳號工具，不在本 repo；沒有它就是「直接模式」，見 README「兩種模式」）、
#          不動資料目錄裡既有的配對、暱稱、燈號與 hook-token 檔，也不關任何既有 tmux session。
#
# 已安裝過的機器，DC_LABEL_PREFIX、DC_ALLOWED_LOGINS、DC_TAILNET、DC_RC_PROFILE 四個可省略，會沿用
# <TARGET_HOME>/.local/share/dev-console/install.env 記下的值；其餘參數不記住，改過預設值時重跑要再給
# （只有「完全沒設定」才沿用；明確設成空值＝清空，例如 DC_RC_PROFILE= 取消常駐、DC_ALLOWED_LOGINS= 會被擋下）。
#
# 必填參數（環境變數；沒填就中止，避免裝出一份「預設放行某人」的服務）：
#   DC_LABEL_PREFIX     launchd label 前綴，例如 com.example.remote；實際 label 為 <前綴>.dev-console.{web,ttyd,rc-<profile>}
#   DC_ALLOWED_LOGINS   允許從 tailnet 登入的 Tailscale 帳號（email，逗號分隔）
#   DC_TAILNET          tailnet 名稱（<名稱>.ts.net 的 <名稱>，例如 example-tailnet）；
#                       正常模式會與 tailscale status 比對，不一致就中止，避免把 serve 開在別的 tailnet
#
# 選填參數：
#   TARGET_HOME         安裝目標家目錄（預設 ${HOME}）；所有產出檔都落在這底下，驗收時指向假 HOME
#   NO_LOAD=1           只產檔不啟動：不執行 launchctl、不跑 tailscale、不動 tmux server、不呼叫 brew
#                       （Homebrew 路徑用 BREW_PREFIX 或依 CPU 推算）；模式記進 install.env，解除時自動沿用
#   TARGET_HOME 不是本帳號真正的家目錄（以 dscl 查，不看 ${HOME}）時一律強制 NO_LOAD=1，NO_LOAD=0 也不生效。
#   TARGET_HOME 不是本機 $HOME 時，go build 的快取（GOCACHE／GOPATH／GOMODCACHE）改放目標 HOME 底下，不寫進真實家目錄
#   DC_TS_HOSTNAME      tailnet 上的本機名稱；正常模式取自 tailscale status，NO_LOAD 模式預設取本機 LocalHostName
#   DC_UI_PORT（7690）、DC_HOOK_PORT（7691）
#   DC_RC_PROFILE       設了才裝手機遙控常駐（Claude remote-control，rc-<名稱>），預設空＝不裝。
#                       aiuse 模式：填要常駐的 aiuse claude profile；直接模式：只當服務名稱，實際以預設帳號執行 claude remote-control
#   DC_AIUSE_PATH       aiuse 位置（預設 <TARGET_HOME>/.local/bin/aiuse）；不存在或不可執行＝直接模式
#   DC_CLAUDE_BIN／DC_CODEX_BIN  直接模式要執行的 claude／codex 絕對路徑（預設從 session PATH 找）
#   ttyd 健康與自動恢復（ADR 0006，README「ttyd 健康與自動恢復」）：
#   DC_TTYD_NOFILE（8192）、DC_TTYD_FD_RESTART（2048）、DC_TTYD_WATCH_INTERVAL（60s；0＝不自動重啟）。
set -euo pipefail
# 結束碼保護：macOS 內建 /bin/bash 3.2 在 set -u 觸發「未設定變數」而中止時，若有 EXIT trap，
# 整支腳本會以 trap 最後一個指令的狀態碼結束（通常是 0），trap 內的 $? 也讀到 0——中途失敗會被當成成功。
# 所以用「完成旗標」：只有走到正常結束點才標記完成；沒標記就一律以非 0 結束（原本就非 0 的保留原值）。
DC_INSTALL_DONE=0
BUILD_OUT=""
on_exit() {
  local rc=$?
  [ -z "$BUILD_OUT" ] || rm -rf "${BUILD_OUT:?}"
  if [ "$DC_INSTALL_DONE" != "1" ]; then
    [ "$rc" -ne 0 ] || rc=1
    exit "$rc"
  fi
}
trap on_exit EXIT

REPO_DIR="$(cd "$(dirname "$0")" && pwd -P)"
TARGET_HOME="${TARGET_HOME:-$HOME}"
NO_LOAD="${NO_LOAD:-0}"

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
PREFIX="$TARGET_HOME/.local/share/dev-console"
BIN_DIR="$PREFIX/bin"
RUN_DIR="$PREFIX/run"
LOG_DIR="$TARGET_HOME/Library/Logs/dev-console"
LA_DIR="$TARGET_HOME/Library/LaunchAgents"
UID_NUM="$(id -u)"
UI_PORT="${DC_UI_PORT:-7690}"
HOOK_PORT="${DC_HOOK_PORT:-7691}"

# 安裝紀錄只拿需要的鍵，不 source（避免檔案被改過時執行任意指令）。
read_install_env() { [ -f "$PREFIX/install.env" ] && sed -n "s/^$1=//p" "$PREFIX/install.env" | head -1 || true; }
# 有設定（含空值）就用設定值，完全沒設才沿用 install.env；declare -p 分得出「沒設」與「設成空值」，macOS 內建 bash 3.2 也可用。
setting() { if declare -p "$1" >/dev/null 2>&1; then printf '%s' "${!1}"; else read_install_env "$1"; fi; }
LABEL_PREFIX_BASE="$(setting DC_LABEL_PREFIX)"
ALLOWED_LOGINS="$(setting DC_ALLOWED_LOGINS)"
TAILNET="$(setting DC_TAILNET)"
RC_PROFILE="$(setting DC_RC_PROFILE)"
TTYD_NOFILE="${DC_TTYD_NOFILE:-8192}"
TTYD_FD_RESTART="${DC_TTYD_FD_RESTART:-2048}"
TTYD_WATCH_INTERVAL="${DC_TTYD_WATCH_INTERVAL:-60s}"

fail() { echo "錯誤：$*" >&2; exit 1; }

# ---------- 0. 參數驗證 ----------
# label 前綴必須是反向網域格式；它會成為 plist 檔名與 launchctl 目標，含空白或斜線會讓解除時刪錯檔。
[[ "$LABEL_PREFIX_BASE" =~ ^[A-Za-z0-9][A-Za-z0-9-]*(\.[A-Za-z0-9][A-Za-z0-9-]*)+$ ]] ||
  fail "DC_LABEL_PREFIX 必填，格式如 com.example.remote（收到「${LABEL_PREFIX_BASE}」）"
[ -n "$ALLOWED_LOGINS" ] || fail "DC_ALLOWED_LOGINS 必填（允許登入的 Tailscale 帳號 email，逗號分隔）"
IFS=',' read -r -a _logins <<<"$ALLOWED_LOGINS"
for _l in "${_logins[@]}"; do
  _l="$(printf '%s' "$_l" | tr -d '[:space:]')"
  [[ "$_l" =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$ ]] || fail "DC_ALLOWED_LOGINS 含不像 email 的值：${_l}"
done
[[ "$TAILNET" =~ ^[a-z0-9][a-z0-9-]*$ ]] || fail "DC_TAILNET 必填，填 <名稱>.ts.net 的 <名稱>（收到「${TAILNET}」）"
# 舊版的 standalone 名單已移除：哪個 profile 走哪種 Codex 協定改由 session 上的標記判斷，不需要名單。
[ -z "${DC_CODEX_STANDALONE_PROFILES:-}" ] ||
  echo "提醒：DC_CODEX_STANDALONE_PROFILES 已不需要（Codex 協定改由 session 標記判斷），本次忽略。" >&2
[ -z "$RC_PROFILE" ] || [[ "$RC_PROFILE" =~ ^[a-z0-9][a-z0-9-]*$ ]] || fail "DC_RC_PROFILE 格式不合法：${RC_PROFILE}"
[[ "$TTYD_NOFILE" =~ ^[0-9]+$ ]] && [ "$TTYD_NOFILE" -ge 1024 ] || fail "DC_TTYD_NOFILE 必須是 ≥1024 的整數"
# 門檻要 >0 且小於上限（等於或超過上限就永遠不會在 EMFILE 之前觸發）。
[[ "$TTYD_FD_RESTART" =~ ^[0-9]+$ ]] && [ "$TTYD_FD_RESTART" -gt 0 ] && [ "$TTYD_FD_RESTART" -lt "$TTYD_NOFILE" ] ||
  fail "DC_TTYD_FD_RESTART 必須是 >0 且小於 DC_TTYD_NOFILE（${TTYD_NOFILE}）的整數"
# 週期要是 Go duration（例：60s、2m、1m30s；0＝關閉自動重啟）；只寫數字少單位時 Go 會解析失敗。
[[ "$TTYD_WATCH_INTERVAL" =~ ^(0|([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h))+)$ ]] ||
  fail "DC_TTYD_WATCH_INTERVAL 必須是 Go duration（例：60s、2m；0＝關閉），收到「${TTYD_WATCH_INTERVAL}」"
# 實際 label 多一段 dev-console：同一個前綴可能也被安裝者其他 plist 使用，
# 解除時只刪 <前綴>.dev-console.* 才不會誤刪別人的 plist。
LABEL_PREFIX="$LABEL_PREFIX_BASE.dev-console"

say() { printf '\n== %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || fail "缺少 $1：$2"; }
[ "$NO_LOAD" = "1" ] && echo "NO_LOAD=1：只產檔，不載入 launchd、不呼叫 tailscale、不動 tmux server。目標家目錄：$TARGET_HOME"

# ---------- 1. 依賴 ----------
say "檢查依賴"
need jq "brew install jq"
if [ "$NO_LOAD" = "1" ]; then
  # 只產檔時不呼叫 brew（連唯讀查詢也不做）；路徑以 Homebrew 慣例依 CPU 推算，正式啟用時重跑會以 brew --prefix 校正。
  if [ -z "${BREW_PREFIX:-}" ]; then
    case "$(uname -m)" in arm64) BREW_PREFIX=/opt/homebrew ;; *) BREW_PREFIX=/usr/local ;; esac
  fi
else
  need brew "請先安裝 Homebrew"
  need go "brew install go"
  need tailscale "請先安裝 Tailscale App 並登入"
  BREW_PREFIX="$(brew --prefix)"
  [ -x "$BREW_PREFIX/bin/tmux" ] || brew install tmux
  if [ -x "$BREW_PREFIX/bin/ttyd" ]; then
    echo "ttyd 已安裝：$("$BREW_PREFIX/bin/ttyd" --version)"
  else
    brew install ttyd
  fi
fi
TMUX_BIN="$BREW_PREFIX/bin/tmux"
TTYD_BIN="$BREW_PREFIX/bin/ttyd"
AIUSE_BIN="${DC_AIUSE_PATH:-$TARGET_HOME/.local/bin/aiuse}"
CLAUDE_BIN_OVERRIDE="${DC_CLAUDE_BIN:-}"
CODEX_BIN_OVERRIDE="${DC_CODEX_BIN:-}"
for _b in "$CLAUDE_BIN_OVERRIDE" "$CODEX_BIN_OVERRIDE"; do
  [ -z "$_b" ] || [[ "$_b" = /* ]] || fail "DC_CLAUDE_BIN／DC_CODEX_BIN 必須是絕對路徑（收到「${_b}」）"
done

mkdir -p "$BIN_DIR" "$LOG_DIR" "$LA_DIR"
mkdir -p "$RUN_DIR" && chmod 700 "$PREFIX" "$RUN_DIR"

# 「內容不同才換」的安裝：tmp + mv 原子替換。
# 不能用 cp/cat 就地覆寫：正在執行的 bash 腳本會從舊位移讀到新內容（見 README「坑」）。
install_if_changed() { # src dst mode → 有更新回 0，沒變回 1
  local src="$1" dst="$2" mode="$3"
  if [ -f "$dst" ] && cmp -s "$src" "$dst"; then
    return 1
  fi
  local tmp
  tmp="$(mktemp "$(dirname "$dst")/.dc-install.XXXXXX")"
  cat "$src" > "$tmp"
  chmod "$mode" "$tmp"
  mv -f "$tmp" "$dst"
  echo "已更新：$dst"
  return 0
}

# ---------- 2. build ----------
say "go build"
WEB_CHANGED=0
TTYD_CHANGED=0
RC_CHANGED=0
if command -v go >/dev/null 2>&1; then
  # 目標 HOME 是別處（例如驗收用假 HOME）時，Go 的建置與模組快取也放到目標 HOME 底下（與 macOS 預設位置同構），
  # 不寫進真實的 ~/Library/Caches/go-build、~/go；-modcacherw 讓模組快取可以被一般 rm 刪掉。
  if [ "$TARGET_HOME" != "$HOME" ]; then
    export GOCACHE="$TARGET_HOME/Library/Caches/go-build" GOPATH="$TARGET_HOME/go"
    export GOMODCACHE="$GOPATH/pkg/mod" GOFLAGS="${GOFLAGS:+$GOFLAGS }-modcacherw"
  fi
  BUILD_OUT="$(mktemp -d)"
  (cd "$REPO_DIR" && go build -trimpath -buildvcs=false -o "$BUILD_OUT/dev-console" ./cmd/dev-console)
  install_if_changed "$BUILD_OUT/dev-console" "$BIN_DIR/dev-console" 755 && WEB_CHANGED=1 || true
else
  # 只有 NO_LOAD 會走到這裡（正常模式在步驟 1 已要求 go）。
  echo "警告：找不到 go，略過 binary 編譯（只產檔模式可接受）" >&2
fi
install_if_changed "$REPO_DIR/scripts/ttyd-attach.sh" "$BIN_DIR/ttyd-attach.sh" 755 || true
install_if_changed "$REPO_DIR/scripts/ttyd-run.sh" "$BIN_DIR/ttyd-run.sh" 755 && TTYD_CHANGED=1 || true
if [ -n "$RC_PROFILE" ]; then
  install_if_changed "$REPO_DIR/scripts/rc-keeper.sh" "$BIN_DIR/rc-keeper.sh" 755 && RC_CHANGED=1 || true
fi

# ---------- 3. Tailscale 資訊 ----------
say "tailnet 主機名"
if [ "$NO_LOAD" = "1" ]; then
  TS_HOST="${DC_TS_HOSTNAME:-$(scutil --get LocalHostName 2>/dev/null | tr '[:upper:]' '[:lower:]' || true)}"
  TS_HOST="${TS_HOST:-mac}"
  PUBLIC_HOST="$TS_HOST.$TAILNET.ts.net"
else
  TS_JSON="$(tailscale status --json)"
  PUBLIC_HOST="$(jq -r '.Self.DNSName // ""' <<<"$TS_JSON" | sed 's/\.$//')"
  [ -n "$PUBLIC_HOST" ] || fail "取不到 tailnet 主機名（Tailscale 已登入嗎？）"
  [ -z "${DC_TS_HOSTNAME:-}" ] || [ "${PUBLIC_HOST%%.*}" = "$DC_TS_HOSTNAME" ] ||
    fail "tailscale 回報的主機名 ${PUBLIC_HOST%%.*} 與 DC_TS_HOSTNAME=${DC_TS_HOSTNAME} 不符"
  # 主機名格式 <host>.<tailnet>.ts.net；比對使用者填的 tailnet，防止登入到別的 tailnet 卻照樣開 serve。
  case "$PUBLIC_HOST" in
    *".$TAILNET.ts.net") ;;
    *) fail "本機目前在 tailnet「${PUBLIC_HOST#*.}」，與 DC_TAILNET=${TAILNET} 不符" ;;
  esac
  # 443 的根路徑 / 已被別的服務佔用時，在動任何服務之前就停下：tailscale serve 會直接覆寫，使用者原本的服務會無聲消失。
  # 只有 / 沒設、或已經指向本服務才往下走；443 上其他路徑（/foo）不受影響，照常共存。
  SERVE_TARGET="http://127.0.0.1:$UI_PORT"
  SERVE_CONFLICT="$(tailscale serve status --json 2>/dev/null | jq -r --arg h "$PUBLIC_HOST:443" --arg t "$SERVE_TARGET" '
      ( .Web[$h].Handlers["/"] // null ) as $r
      | ( .TCP["443"] // null ) as $tcp
      | if $r != null and ($r.Proxy // "") != $t then "https://\($h)/ 已指向 \($r.Proxy // $r.Path // $r.Text // "其他內容")"
        elif $tcp != null and ($tcp.HTTPS // false) != true then "443 已被 TCP 轉送使用（\($tcp.TCPForward // "未知")）"
        else "" end' 2>/dev/null || true)"
  [ -z "$SERVE_CONFLICT" ] || fail "tailscale serve 衝突：${SERVE_CONFLICT}。本工具不會覆寫你的設定；請先自行移除或搬到其他路徑／port（tailscale serve status 可查），再重跑。"
fi
echo "主機：$PUBLIC_HOST"

# launchd 預設 PATH 只有 /usr/bin:/bin:/usr/sbin:/sbin；新開的 claude/codex session 會繼承它，
# 所以這裡明確給完整 PATH（tmux server 若由 launchd 這邊先起，全域環境也會是這份）。
SESSION_PATH="$TARGET_HOME/.local/bin:$BREW_PREFIX/bin:$BREW_PREFIX/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
LANG_VALUE="${LANG:-en_US.UTF-8}"

# ---------- 3.5 模式：有沒有 aiuse ----------
# 與服務端同一個判斷（Config.AiuseAvailable）：設定的 aiuse 是存在、可執行的一般檔案＝aiuse 模式，否則＝直接模式。
# 服務每次新開 session 都重新判斷，之後才裝 aiuse 不必重裝；這裡只是提早說明並檢查直接模式需要的指令。
# tool_bin <tool> <覆寫路徑>：照服務端的規則找執行檔（有覆寫只認覆寫；否則在 session PATH 的絕對目錄找）。
tool_bin() {
  local tool="$1" override="$2" dir
  if [ -n "$override" ]; then
    if [ -f "$override" ] && [ -x "$override" ]; then printf '%s' "$override"; fi
    return 0
  fi
  IFS=':' read -r -a _dirs <<<"$SESSION_PATH"
  for dir in "${_dirs[@]}"; do
    [[ "$dir" = /* ]] || continue
    if [ -f "$dir/$tool" ] && [ -x "$dir/$tool" ]; then printf '%s' "$dir/$tool"; return 0; fi
  done
  return 0
}
say "模式"
if [ -f "$AIUSE_BIN" ] && [ -x "$AIUSE_BIN" ]; then
  MODE=aiuse
  echo "aiuse 模式：${AIUSE_BIN}（網頁新開 session 時選 profile）"
else
  MODE=direct
  echo "直接模式：找不到可執行的 ${AIUSE_BIN}。網頁新開 session 會直接執行 claude／codex，不顯示 profile 選單（README「兩種模式」）。"
  CLAUDE_FOUND="$(tool_bin claude "$CLAUDE_BIN_OVERRIDE")"
  CODEX_FOUND="$(tool_bin codex "$CODEX_BIN_OVERRIDE")"
  echo "  claude：${CLAUDE_FOUND:-找不到（網頁開 Claude 會回錯誤）}"
  echo "  codex ：${CODEX_FOUND:-找不到（網頁開 Codex 會回錯誤）}"
  if [ -n "$RC_PROFILE" ] && [ -z "$CLAUDE_FOUND" ]; then
    # 手機遙控常駐在直接模式下執行 claude remote-control；沒有 claude 時裝上去只會讓常駐 session 一直失敗。
    if [ "$NO_LOAD" = "1" ]; then
      echo "警告：DC_RC_PROFILE 已設定但找不到 claude；只產檔模式照常產生，正式啟用前請先安裝 claude 或設 DC_CLAUDE_BIN。" >&2
    else
      fail "DC_RC_PROFILE 已設定，但直接模式找不到 claude（session PATH：${SESSION_PATH}）。請先安裝 Claude Code、設 DC_CLAUDE_BIN，或以 DC_RC_PROFILE= 取消常駐後重跑。"
    fi
  fi
fi

# ---------- 4. launchd plist ----------
xml_escape() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }

render_plist() { # label program env_pairs...
  local label="$1" program="$2"
  shift 2
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${label}</string>
  <key>ProgramArguments</key>
  <array><string>$(printf '%s' "$program" | xml_escape)</string></array>
  <key>EnvironmentVariables</key>
  <dict>
EOF
  local pair
  for pair in "$@"; do
    printf '    <key>%s</key><string>%s</string>\n' "${pair%%=*}" "$(printf '%s' "${pair#*=}" | xml_escape)"
  done
  cat <<EOF
  </dict>
  <key>WorkingDirectory</key><string>$(printf '%s' "$TARGET_HOME" | xml_escape)</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>$(printf '%s' "$LOG_DIR/${label##*.}.log" | xml_escape)</string>
  <key>StandardErrorPath</key><string>$(printf '%s' "$LOG_DIR/${label##*.}.log" | xml_escape)</string>
${PLIST_EXTRA:-}</dict>
</plist>
EOF
}

# ttyd 的 fd 上限：launchd 預設 soft limit 只有 256，ttyd 每條連線約 4～5 個 fd，
# 加上萬一子程序卡住會漏 fd（ADR 0006），256 太容易撞到。soft 與 hard 都寫，避免 hard 比 soft 低時 launchd 拒套。
TTYD_LIMITS_XML="  <key>SoftResourceLimits</key><dict><key>NumberOfFiles</key><integer>${TTYD_NOFILE}</integer></dict>
  <key>HardResourceLimits</key><dict><key>NumberOfFiles</key><integer>${TTYD_NOFILE}</integer></dict>
"

COMMON_ENV=(
  "HOME=$TARGET_HOME"
  "PATH=$SESSION_PATH"
  "LANG=$LANG_VALUE"
  "DC_TMUX_PATH=$TMUX_BIN"
  "DC_AIUSE_PATH=$AIUSE_BIN"
)
# 直接模式的執行檔覆寫只在有設時寫進 plist；沒設就讓服務自己從 PATH 找（既有安裝的 plist 內容也因此不變）。
[ -z "$CLAUDE_BIN_OVERRIDE" ] || COMMON_ENV+=("DC_CLAUDE_BIN=$CLAUDE_BIN_OVERRIDE")
[ -z "$CODEX_BIN_OVERRIDE" ] || COMMON_ENV+=("DC_CODEX_BIN=$CODEX_BIN_OVERRIDE")

is_loaded() { launchctl print "gui/$UID_NUM/$1" >/dev/null 2>&1; }

ensure_agent() { # label program code_changed env...
  local label="$1" program="$2" code_changed="$3"
  shift 3
  local dst="$LA_DIR/$label.plist" tmp changed=0
  tmp="$(mktemp)"
  render_plist "$label" "$program" "$@" > "$tmp"
  plutil -lint "$tmp" >/dev/null
  install_if_changed "$tmp" "$dst" 644 && changed=1 || true
  rm -f "${tmp:?}"
  if [ "$NO_LOAD" = "1" ]; then
    [ "$changed" = "1" ] || echo "$label 未變更"
    echo "（NO_LOAD）未載入 $label"
    return 0
  fi
  if [ "$changed" = "1" ]; then
    is_loaded "$label" && launchctl bootout "gui/$UID_NUM/$label" 2>/dev/null || true
    # bootout 是非同步的，等它真的卸載再 bootstrap，否則會 "Bootstrap failed: 5"。
    for _ in $(seq 1 20); do is_loaded "$label" || break; sleep 0.25; done
    launchctl bootstrap "gui/$UID_NUM" "$dst"
    echo "已載入 $label"
  elif ! is_loaded "$label"; then
    launchctl bootstrap "gui/$UID_NUM" "$dst"
    echo "已載入 $label"
  elif [ "$code_changed" = "1" ]; then
    launchctl kickstart -k "gui/$UID_NUM/$label"
    echo "程式更新，已重啟 $label"
  else
    echo "$label 未變更"
  fi
}

say "launchd 服務"
ensure_agent "$LABEL_PREFIX.web" "$BIN_DIR/dev-console" "$WEB_CHANGED" "${COMMON_ENV[@]}" \
  "DC_LISTEN_ADDR=127.0.0.1:$UI_PORT" \
  "DC_HOOK_ADDR=127.0.0.1:$HOOK_PORT" \
  "DC_TTYD_SOCKET=$RUN_DIR/ttyd.sock" \
  "DC_PUBLIC_HOST=$PUBLIC_HOST" \
  "DC_LOCAL_HOST=dev-console.localhost" \
  "DC_ALLOWED_LOGINS=$ALLOWED_LOGINS" \
  "DC_SESSION_PATH=$SESSION_PATH" \
  "DC_DATA_DIR=$PREFIX" \
  "DC_PROFILES_ROOT=$TARGET_HOME/.ai-profiles" \
  "DC_CLAUDE_HOME=$TARGET_HOME/.claude" \
  "DC_TTYD_LABEL=$LABEL_PREFIX.ttyd" \
  "DC_TTYD_NOFILE=$TTYD_NOFILE" \
  "DC_TTYD_FD_RESTART=$TTYD_FD_RESTART" \
  "DC_TTYD_WATCH_INTERVAL=$TTYD_WATCH_INTERVAL"
# 只有 ttyd 的 plist 帶 fd 上限；以指令前綴的環境變數傳給 render_plist，不影響其他服務。
PLIST_EXTRA="$TTYD_LIMITS_XML" ensure_agent "$LABEL_PREFIX.ttyd" "$BIN_DIR/ttyd-run.sh" "$TTYD_CHANGED" "${COMMON_ENV[@]}" \
  "DC_TTYD_SOCKET=$RUN_DIR/ttyd.sock" \
  "DC_TTYD_BIN=$TTYD_BIN" \
  "DC_BIN_DIR=$BIN_DIR"
if [ -n "$RC_PROFILE" ]; then
  ensure_agent "$LABEL_PREFIX.rc-$RC_PROFILE" "$BIN_DIR/rc-keeper.sh" "$RC_CHANGED" "${COMMON_ENV[@]}" \
    "DC_RC_SESSION=rc-$RC_PROFILE" \
    "DC_RC_PROFILE=$RC_PROFILE"
fi

# 記下這次安裝的設定：解除時不必再問一次參數、不會用錯前綴刪到別人的 plist；重跑安裝時沒給的參數也沿用這裡。
# 這是安裝者的個人值（允許登入的帳號、tailnet 名稱），只存在本機 0600 檔，不在 repo 裡。
# DC_INSTALL_MODE：generate＝只產檔、load＝正式啟用；解除時據此決定要不要呼叫 launchctl／tailscale。
# 所有值在步驟 0 都已以白名單格式驗過（不含換行），可以逐行寫入；先寫同目錄暫存檔再 mv，權限從一開始就是 0600。
if [ "$NO_LOAD" = "1" ]; then INSTALL_MODE=generate; else INSTALL_MODE=load; fi
ENV_TMP="$(mktemp "$PREFIX/.install.env.XXXXXX")"
chmod 600 "$ENV_TMP"
cat > "$ENV_TMP" <<EOF
DC_LABEL_PREFIX=$LABEL_PREFIX_BASE
DC_ALLOWED_LOGINS=$ALLOWED_LOGINS
DC_TAILNET=$TAILNET
DC_RC_PROFILE=$RC_PROFILE
DC_INSTALL_MODE=$INSTALL_MODE
TARGET_HOME=$TARGET_HOME
DC_UI_PORT=$UI_PORT
EOF
mv -f "$ENV_TMP" "$PREFIX/install.env"

# ---------- 5. Claude hooks ----------
say "合併 Claude hooks"
TARGET_HOME="$TARGET_HOME" bash "$REPO_DIR/scripts/merge-hooks.sh" install "$HOOK_PORT"

# ---------- 7. tmux ----------
say "tmux window-size"
if [ "$NO_LOAD" = "1" ]; then
  echo "（NO_LOAD）略過：正式啟用時會確認 tmux window-size=latest"
else
  # 多個畫面（Mac 螢幕／SSH／手機網頁）同時 attach 時，以最近操作的那個尺寸為準，不被縮成最小視窗。
  # tmux 3.1+ 預設就是 latest；這裡只檢查，不是 latest 才在 ~/.tmux.conf 補一行。
  WS="$("$TMUX_BIN" start-server \; show-options -gv window-size 2>/dev/null || echo unknown)"
  if [ "$WS" = "latest" ]; then
    echo "window-size=latest"
  else
    grep -qs '^set -g window-size latest' "$TARGET_HOME/.tmux.conf" || echo 'set -g window-size latest' >> "$TARGET_HOME/.tmux.conf"
    "$TMUX_BIN" set-option -g window-size latest 2>/dev/null || true
    echo "已設定 window-size=latest（原為 ${WS}）"
  fi
fi

# ---------- 8. tailscale serve ----------
say "tailscale serve"
TARGET="http://127.0.0.1:$UI_PORT"
if [ "$NO_LOAD" = "1" ]; then
  echo "（NO_LOAD）未執行。正式啟用時會跑：tailscale serve --bg --https=443 $TARGET"
else
  SERVE_JSON="$(tailscale serve status --json 2>/dev/null || echo '{}')"
  CUR="$(jq -r --arg h "$PUBLIC_HOST:443" '.Web[$h].Handlers["/"].Proxy // ""' <<<"$SERVE_JSON")"
  if [ "$CUR" = "$TARGET" ]; then
    echo "serve 已設定：https://$PUBLIC_HOST/ → $TARGET"
  elif [ -n "$CUR" ]; then
    # 步驟 3 已檢查過；這裡再擋一次，防止安裝途中有人改了 serve 設定而被本工具覆寫。
    fail "tailscale serve 衝突：https://$PUBLIC_HOST/ 已指向 ${CUR}，本工具不會覆寫；請先自行處理後重跑。"
  else
    # tailnet 尚未啟用 Serve／HTTPS 憑證時，tailscale serve 會印出啟用網址後無限等待；
    # 這裡背景執行、最多等 20 秒，看到提示就停掉並把網址印給人，不讓安裝卡死。
    # 先清掉前幾次安裝逾時留下的 serve 等待程序：它們一直在等授權，使用者按下啟用時可能套到舊設定。
    pkill -f "tailscale serve --bg --https=443 $TARGET" 2>/dev/null || true
    SERVE_OUT="$(mktemp)"
    tailscale serve --bg --https=443 "$TARGET" >"$SERVE_OUT" 2>&1 &
    SERVE_PID=$!
    for _ in $(seq 1 40); do
      kill -0 "$SERVE_PID" 2>/dev/null || break
      grep -q 'not enabled' "$SERVE_OUT" && break
      sleep 0.5
    done
    if kill -0 "$SERVE_PID" 2>/dev/null; then
      # /usr/local/bin/tailscale 是 sh wrapper，真正等待的是它的子程序（App 內的 tailscale）；
      # 只 kill wrapper 會留下孤兒，所以先 kill 子程序，再收 wrapper，最後以指令樣式兜底。
      pkill -P "$SERVE_PID" 2>/dev/null || true
      kill "$SERVE_PID" 2>/dev/null || true
      wait "$SERVE_PID" 2>/dev/null || true
      pkill -f "tailscale serve --bg --https=443 $TARGET" 2>/dev/null || true
      cat "$SERVE_OUT"
      echo "警告：tailnet 尚未啟用 Serve（HTTPS 憑證）。到上面網址按啟用後重跑 install.sh。" >&2
      echo "      注意：啟用 HTTPS 憑證會讓 tailnet 機器名稱出現在公開的 Certificate Transparency 紀錄。" >&2
      SERVE_PENDING=1
    else
      wait "$SERVE_PID" || true
      cat "$SERVE_OUT"
    fi
    rm -f "${SERVE_OUT:?}"
  fi
  if [ "$(jq -r '.AllowFunnel // {} | length' <<<"$(tailscale serve status --json 2>/dev/null || echo '{}')")" != "0" ]; then
    echo "警告：funnel 是開的！終端機不可對公網，請執行 tailscale funnel reset" >&2
  fi
fi

# ---------- 9. 健檢 ----------
say "健檢"
if [ "$NO_LOAD" = "1" ]; then
  for f in "$LA_DIR/$LABEL_PREFIX".*.plist; do plutil -lint "$f"; done
  echo "只產檔完成（${MODE} 模式）；服務未啟動。正式啟用：照 README「安裝」，在本機家目錄不帶 NO_LOAD 重跑 ./install.sh（DC_LABEL_PREFIX、DC_ALLOWED_LOGINS、DC_TAILNET、DC_RC_PROFILE 沿用 install.env，其餘參數要再給）。"
  DC_INSTALL_DONE=1
  exit 0
fi
for _ in $(seq 1 20); do
  curl -fsS -m 2 "http://127.0.0.1:$UI_PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -fsS -m 2 "http://127.0.0.1:$UI_PORT/healthz" >/dev/null && echo "總覽服務 OK（127.0.0.1:${UI_PORT}）" || echo "總覽服務尚未回應，看 $LOG_DIR/web.log" >&2
[ -S "$RUN_DIR/ttyd.sock" ] && echo "ttyd socket OK" || echo "ttyd socket 尚未出現，看 $LOG_DIR/ttyd.log" >&2

if [ "${SERVE_PENDING:-0}" = "1" ]; then
  echo "tailscale serve 尚未生效（見上方啟用網址）；本機服務已在 127.0.0.1:${UI_PORT} 運作。"
fi

cat <<EOF

完成（${MODE} 模式）。網址：https://$PUBLIC_HOST/　本機：http://dev-console.localhost:${UI_PORT}/
每台瀏覽器第一次要配對：$BIN_DIR/dev-console pair
提醒（不自動執行）：
  - 讓這台不休眠：sudo pmset -c sleep 0 disksleep 0；或常駐 caffeinate -s
  - 手機要連得到：TS_API_TOKEN=... $REPO_DIR/scripts/tailscale-acl.sh --src <你的 Tailscale 帳號>（本機掛 tag 時才需要）
EOF
DC_INSTALL_DONE=1
