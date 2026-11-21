#!/bin/bash
# 驗證「只產檔／目標不是本帳號家目錄」時，install.sh 與 uninstall.sh 完全不呼叫真機的 tmux、launchctl、tailscale、brew、ttyd。
#
# 做法：在暫存目錄放同名假指令（只把呼叫記到 calls.log，不做任何事），放在 PATH 最前面，
# 並把 BREW_PREFIX 指到假指令目錄（install.sh 以 $BREW_PREFIX/bin/tmux 的絕對路徑呼叫 tmux）。
# 跑完 calls.log 必須是空的；任何一筆都代表有路徑漏了真機保護。
# 本腳本自己不呼叫 tmux、launchctl、tailscale；產物全部在 mktemp 目錄內，結束時刪除。
#
# 情境：
#   1. HOME 與 TARGET_HOME 都是假目錄、NO_LOAD=1：install → uninstall ×2
#   2. HOME 與 TARGET_HOME 都是假目錄、明確 NO_LOAD=0：install.sh／uninstall.sh 必須自行判定不是本帳號家目錄而改為只處理檔案
#   3. DC_ALLOWED_LOGINS 空值：install.sh 必須非 0 結束
#   4、5. 有 aiuse＋LANG=zh_TW.UTF-8／en_US.UTF-8：aiuse 模式完整跑完（install.env＋3 個 plist）
#   6. 安裝中途失敗（寫檔失敗、trap 之後的未設定變數）：install.sh 必須非 0 結束
# 用法：scripts/test-install-isolation.sh（需要 go、jq）
# 受測腳本一律直接執行（走 shebang 的 /bin/bash，也就是 macOS 內建的 bash 3.2），不用 PATH 上的 bash：
# Homebrew 的 bash 5 不會重現 3.2 的 UTF-8 變數名與 EXIT trap 狀態碼問題，用它跑會讓測試假綠。
set -u
REPO="$(cd "$(dirname "$0")/.." && pwd -P)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/rc-isolation.XXXXXX")"
# 結束碼保護：macOS 內建 /bin/bash 3.2 在 set -u 觸發「未設定變數」而中止時，若有 EXIT trap，
# 整支腳本會以 trap 最後一個指令的狀態碼結束（通常是 0），trap 內的 $? 也讀到 0——中途失敗會被當成成功。
# 所以用「完成旗標」：只有走到正常結束點才標記完成；沒標記就一律以非 0 結束（原本就非 0 的保留原值）。
TEST_DONE=0
on_exit() {
  local rc=$?
  rm -rf "${WORK:?}"
  if [ "$TEST_DONE" != "1" ]; then
    [ "$rc" -ne 0 ] || rc=1
    exit "$rc"
  fi
}
trap on_exit EXIT
STUB="$WORK/stub"
LOG="$WORK/calls.log"
mkdir -p "$STUB/bin"
: > "$LOG"
for cmd in tmux launchctl tailscale brew ttyd; do
  # 假指令只記錄、不轉呼叫真的指令；brew --prefix 回假目錄，讓後續絕對路徑也落在假指令上。
  cat > "$STUB/bin/$cmd" <<EOF
#!/bin/sh
printf '%s %s\n' "$cmd" "\$*" >> "$LOG"
[ "$cmd" = brew ] && [ "\$1" = --prefix ] && printf '%s\n' "$STUB"
exit 0
EOF
  chmod 755 "$STUB/bin/$cmd"
done
GO_DIR="$(dirname "$(command -v go)")"
JQ_DIR="$(dirname "$(command -v jq)")"
SAFE_PATH="$STUB/bin:$GO_DIR:$JQ_DIR:/usr/bin:/bin:/usr/sbin:/sbin"

fail=0
check() { # 說明 預期結束碼 實際結束碼
  if [ "$2" = "$3" ]; then echo "PASS：$1（exit $3）"; else echo "FAIL：$1（預期 exit $2，實際 $3）"; fail=1; fi
}
run_in() { # 假家目錄 指令 [環境變數…]
  local home="$1" script="$2"
  shift 2
  env -i HOME="$home" PATH="$SAFE_PATH" TARGET_HOME="$home" BREW_PREFIX="$STUB" LANG=en_US.UTF-8 "$@" \
    "$REPO/$script" >> "$WORK/run.log" 2>&1
}
COMMON=(DC_LABEL_PREFIX=com.example.isolation DC_ALLOWED_LOGINS=friend@example.com DC_TAILNET=example-tailnet DC_RC_PROFILE=main)

H1="$WORK/home1"
mkdir -p "$H1"
run_in "$H1" install.sh NO_LOAD=1 "${COMMON[@]}"; check "情境 1 install（NO_LOAD=1）" 0 $?
run_in "$H1" uninstall.sh; check "情境 1 uninstall 第 1 次" 0 $?
run_in "$H1" uninstall.sh; check "情境 1 uninstall 第 2 次" 0 $?

H2="$WORK/home2"
mkdir -p "$H2"
run_in "$H2" install.sh NO_LOAD=0 "${COMMON[@]}"; check "情境 2 install（NO_LOAD=0，應自動改只產檔）" 0 $?
grep -q '^DC_INSTALL_MODE=generate$' "$H2/.local/share/dev-console/install.env" 2>/dev/null
check "情境 2 install.env 記為只產檔" 0 $?
run_in "$H2" uninstall.sh NO_LOAD=0; check "情境 2 uninstall（NO_LOAD=0，應自動改只刪檔）" 0 $?

H3="$WORK/home3"
mkdir -p "$H3"
run_in "$H3" install.sh NO_LOAD=1 DC_LABEL_PREFIX=com.example.isolation DC_ALLOWED_LOGINS= DC_TAILNET=example-tailnet
check "情境 3 DC_ALLOWED_LOGINS 空值被擋" 1 $?

# 情境 4、5：有 aiuse（假檔）＋UTF-8 語系。macOS 內建 bash 3.2 在 UTF-8 下會把「$變數」後面緊接的全形字併進變數名，
# set -u 時中途中止；這裡確認 aiuse 模式能完整跑完並產生 install.env 與三個 plist。
for loc in zh_TW.UTF-8 en_US.UTF-8; do
  H="$WORK/home-aiuse-$loc"
  mkdir -p "$H/.local/bin"
  printf '#!/bin/sh\nexit 0\n' > "$H/.local/bin/aiuse"
  chmod 755 "$H/.local/bin/aiuse"
  : > "$WORK/run-$loc.log"
  env -i HOME="$H" PATH="$SAFE_PATH" TARGET_HOME="$H" BREW_PREFIX="$STUB" LANG="$loc" NO_LOAD=1 "${COMMON[@]}" \
    "$REPO/install.sh" > "$WORK/run-$loc.log" 2>&1
  check "情境 aiuse＋LANG=$loc install" 0 $?
  n_plist="$(find "$H/Library/LaunchAgents" -name 'com.example.isolation.dev-console.*.plist' 2>/dev/null | wc -l | tr -d ' ')"
  [ -f "$H/.local/share/dev-console/install.env" ] && [ "$n_plist" = 3 ] && grep -q 'aiuse 模式' "$WORK/run-$loc.log"
  check "情境 aiuse＋LANG=$loc 產生 install.env、3 個 plist、模式為 aiuse（plist $n_plist 個）" 0 $?
  cat "$WORK/run-$loc.log" >> "$WORK/run.log"
done

# 情境 6：安裝中途失敗必須回非 0。
#   6a 一般指令失敗：LaunchAgents 目錄唯讀，寫 plist 失敗。
H6="$WORK/home-fail"
mkdir -p "$H6/Library/LaunchAgents"
chmod 500 "$H6/Library/LaunchAgents"
run_in "$H6" install.sh NO_LOAD=1 "${COMMON[@]}"; rc=$?
[ "$rc" -ne 0 ]; check "情境 6a 寫 plist 失敗時 install 回非 0（實際 ${rc}）" 0 $?
chmod 700 "$H6/Library/LaunchAgents"
#   6b 未設定變數（set -u）：複製一份 install.sh，在設好 EXIT trap 之後插入一行引用未設定變數，
#      模擬日後任何 unbound 錯誤；bash 3.2 在這種情況會以 EXIT trap 的狀態碼結束，必須仍是非 0。
BROKEN="$WORK/repo-broken"
mkdir -p "$BROKEN"
(cd "$REPO" && tar -cf - --exclude .git .) | tar -xf - -C "$BROKEN"
perl -0pi -e 's/^(\s*trap .* EXIT\n)/$1  echo "\$\{DC_INTENTIONALLY_UNSET_FOR_TEST\}"\n/m' "$BROKEN/install.sh"
grep -q 'DC_INTENTIONALLY_UNSET_FOR_TEST' "$BROKEN/install.sh"; check "情境 6b 已在 trap 之後插入未設定變數" 0 $?
H6B="$WORK/home-unbound"
mkdir -p "$H6B"
env -i HOME="$H6B" PATH="$SAFE_PATH" TARGET_HOME="$H6B" BREW_PREFIX="$STUB" LANG=en_US.UTF-8 NO_LOAD=1 "${COMMON[@]}" \
  "$BROKEN/install.sh" >> "$WORK/run.log" 2>&1; rc=$?
[ "$rc" -ne 0 ]; check "情境 6b trap 之後的未設定變數使 install 回非 0（實際 ${rc}）" 0 $?

if [ -s "$LOG" ]; then
  echo "FAIL：假指令被呼叫（代表會碰到真機）："
  cat "$LOG"
  fail=1
else
  echo "PASS：tmux／launchctl／tailscale／brew／ttyd 呼叫紀錄為空"
fi
[ "$fail" = 0 ] || { echo "--- 執行紀錄"; cat "$WORK/run.log"; }
TEST_DONE=1
exit "$fail"
