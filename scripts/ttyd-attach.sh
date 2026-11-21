#!/bin/bash
# ttyd 每條網頁連線會執行一次本腳本，參數來自 URL 的 ?arg=<session>。
# 這裡是第二道驗證（Go 反代已驗過一次）：名稱白名單＋session 必須存在，然後 attach。
# 名稱永遠以 "=" 前綴做精確比對，也不經任何 shell 展開。
set -u
TMUX_BIN="${DC_TMUX_PATH:-/opt/homebrew/bin/tmux}"
# 丟掉 tty 輸出佇列裡還沒被讀走的資料（原因見下方「不能 exec tmux」）。
flush_out() { /usr/bin/perl -MPOSIX -e 'POSIX::tcflush(0, POSIX::TCOFLUSH)' 2>/dev/null || true; }
child=""
hup_at=""
# trap 必須在任何其他動作之前設好：ttyd 一連上就送控制訊息，使用者開了馬上關時 HUP 可能很早就到，
# trap 還沒設的話 bash 會被直接殺掉，後面的清佇列與等待都不會跑。
# 斷線瞬間輸出佇列可能已滿（瀏覽器送 PAUSE、ttyd 停讀），tmux 收 HUP 後寫還原控制碼或
# tcsetattr(TCSAFLUSH) 會在結束前就卡住；所以先清佇列再轉送 HUP。ttyd 的 HUP 本來就送到整個群組，
# 這裡再轉一次也涵蓋只送給 bash 的 TERM。
on_hup() {
  flush_out
  [ -n "$child" ] && kill -HUP "$child" 2>/dev/null
  [ -n "$hup_at" ] || hup_at=$SECONDS
}
trap on_hup HUP TERM
name="${1:-}"
if [ -n "$hup_at" ]; then
  exit 0
fi
if [ "$#" -ne 1 ] || ! [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,119}$ ]]; then
  echo "dev-console：session 名稱不合法。"
  sleep 3
  exit 1
fi
if ! "$TMUX_BIN" has-session -t "=$name" 2>/dev/null; then
  echo "dev-console：找不到 tmux session「${name}」。"
  sleep 3
  exit 1
fi
# ttyd 由 launchd 啟動，不在 tmux 內；保險起見清掉 TMUX，避免被當成巢狀而拒絕 attach。
unset TMUX
# 不能 exec tmux（ADR 0006）：網頁斷線時 ttyd 1.7.7 停讀 pty、對整個 process group 送 SIGHUP，
# 再等子程序結束才 close master。tmux client 收到 HUP 會寫出還原終端的控制碼，而 macOS 最後一次
# close tty 時要等輸出排空（沒有逾時）——沒人讀 master 就永遠排不空：tmux 卡在 E 狀態、ttyd 每次漏 3 個 fd。
# 所以讓 bash 留著同一個 tty：tmux 在背景跑，結束後由 bash 丟掉還沒被讀走的輸出（TCOFLUSH），
# 最後一次 close 就沒有東西要排空。
# 非互動 bash 的背景工作 stdin 會被導到 /dev/null，所以要明寫 <&0。tmux 與 bash 同一個
# process group（非互動 shell 不開 job control），是 tty 的前景群組，視窗縮放的 SIGWINCH 照樣送得到 tmux。
"$TMUX_BIN" attach-session -t "=$name" <&0 &
child=$!
# 斷線後最多等 tmux 這麼多秒，逾時就 KILL。
HUP_GRACE=3
# 平常 wait 到 tmux 結束（session 結束、detach）；wait 被 trap 打斷後改成輪詢：每輪清一次佇列
# （tmux 可能又寫滿），HUP_GRACE 秒後仍未結束就 KILL，保證一定會走到下面的 exit，ttyd 收得到子程序結束。
# sleep 把 stdio 導到 /dev/null，不多佔 tty。
while kill -0 "$child" 2>/dev/null; do
  if [ -z "$hup_at" ]; then
    wait "$child"
    continue
  fi
  # HUP 若在 tmux 啟動前就到（child 還是空的），這裡補送；重複送無害。
  flush_out
  kill -HUP "$child" 2>/dev/null
  if (( SECONDS - hup_at >= HUP_GRACE )); then
    kill -KILL "$child" 2>/dev/null
  fi
  sleep 0.2 </dev/null >/dev/null 2>&1
done
# bash 會記住已回收背景工作的結束碼；session 正常結束時沿用 tmux 的結束碼。
wait "$child" 2>/dev/null
rc=$?
# 最後一次 close tty 是 bash 的；先清空佇列，close 就沒有東西要排空。
flush_out
exit "$rc"
