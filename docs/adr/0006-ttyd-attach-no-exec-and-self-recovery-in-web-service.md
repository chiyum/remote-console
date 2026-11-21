# ADR 0006：ttyd-attach 不 exec tmux；ttyd 自動恢復放在 web 服務內，以反代追蹤的 WebSocket 數判斷有無連線

- 狀態：已採用（2026-10-28）
- 決策者：擁有者（修正方向、fd 上限、閒置才重啟、異常提示與退避）、architect（自動恢復放置位置、連線判定、前端退避做法）

## 背景

正式環境 84 次連線後 ttyd 漏了 240 個 fd，撞到 launchd 預設 soft limit 256，之後每次 `pty_spawn` 都 `Too many open files`，ttyd 前端不停重連，web.log 每秒多筆 `[term] ttyd 連線失敗：EOF`。

根因：網頁斷線時 ttyd 1.7.7 停讀 pty、對子程序群組送 SIGHUP，在 thread 裡 waitpid，等到才 close master（`src/pty.c` `process_free`）。原本 `ttyd-attach.sh` 最後 `exec tmux attach`，tmux client 收到 HUP 會寫還原終端的控制碼；macOS 最後一次 close tty 時 `ttywflush` 等輸出排空、沒有逾時，而 master 沒人讀，於是死結：tmux 永遠卡在 `E` 狀態，ttyd 每次漏 master＋2 個 dup。瀏覽器 xterm 寫不完時送 PAUSE 讓 ttyd 停讀 pty，輸出佇列先被塞住，手機最容易觸發。上游 1.7.7 是最新版、未修。

## 決策

1. **根因修正**：`ttyd-attach.sh` 改成 tmux 在背景跑（`<&0` 明寫 stdin），bash 留著同一個 tty、trap HUP／TERM 先 `tcflush(0, TCOFLUSH)` 再轉給 tmux（輸出佇列已滿時 tmux 寫還原碼或 `tcsetattr(TCSAFLUSH)` 會在結束前卡住），之後每 0.2 秒再清一次、3 秒未結束就 KILL（trap 在腳本第一步就設好：ttyd 一連上就送控制訊息，開了立刻關時 HUP 可能早於 trap，bash 會被直接殺掉）；tmux 結束後再清一次才退出，最後一次 close 沒有東西要排空。結束碼沿用 tmux 的（session 正常結束為 0）。
2. **fd 上限**：install.sh 給 ttyd plist 寫 Soft／Hard `NumberOfFiles`（預設 8192）。
3. **自動恢復放在 web 服務內的 goroutine**（`internal/ttydwatch`）：每個週期以 `launchctl print` 取 pid、`lsof` 數 fd；達門檻且「沒有連線」才 `launchctl kickstart -k`。
4. **「有連線」＝反代追蹤的活 WebSocket 數**：ttyd 只聽 0700 目錄裡的 unix socket，唯一連入者是本服務的反代；反代升級後 `ServeHTTP` 阻塞到連線結束，前後加減計數就是 ttyd 的實際連線數。檢查與進入重啟在同一把鎖內，重啟期間新 WebSocket 回 503，等新 socket 有回應才放行。
5. **前端退避**：ttyd 加 `-t disableReconnect=true`（它的前端斷線後立刻重連、沒有退避）。反代注入同步 script `termguard.js`，包住 WebSocket，不正常關閉時改查 `/api/term/health`、顯示「終端機服務異常」、指數退避，恢復後重新載入頁面；ttyd 連不上時 index 回 503 替代頁，同一支 script 接手。
6. **健康狀態**：同一套判斷給 `/api/term/health`、總覽頁標籤與 `dev-console ttyd-status`（`scripts/check.sh` 呼叫）。`ok=false` 只代表「現在開終端機會失敗」（沒在執行、socket 不回應、讀不到 fd、逼近上限、重啟中）；超過自動重啟門檻只是 `warning`——否則有人連著、暫不重啟的期間，其他分頁會卡在異常提示、check.sh 假紅。讀不到 fd 數算異常，不當 0 假綠。

## 考慮過的其他做法

- **獨立 launchd job／shell 腳本做自動恢復**：要另外判斷「有沒有連線」，只能看 ttyd 子程序或 lsof 的 socket 連線；子程序在洩漏情境下本來就有卡死的殘留，會一直被當成「有人在用」而永不重啟。還要多一個 plist 與 log。
- **以 ttyd 子程序存在判斷有連線**：同上，殘留子程序造成誤判。
- **保留 ttyd 前端重連，只在 Go 端擋**：ttyd 前端沒有退避，Go 端回什麼它都會立刻再連；要控制頻率只能在瀏覽器端。
- **呼叫 ttyd 內部的 connect（`window.term`）而不重新載入**：依賴壓縮過的 bundle 內部方法，ttyd 升版最容易壞；重新載入只依賴公開網址。
- **升級／fork ttyd**：非目標；上游未修，fork 要長期維護。

## 取捨

- 自動恢復與 web 服務同生命週期：web 服務掛掉時不會自動重啟 ttyd，但那時終端機本來就連不上（反代在 web 裡），launchd 會先把 web 拉起來。
- 計數只在 web 服務的記憶體裡；web 重啟時計數歸零，但那時所有反代連線也都斷了，0 是正確的。
- 檢查到重啟之間有幾毫秒，新連線會被 503，前端照退避重試。
- 重新載入頁面會丟掉 xterm 的捲動紀錄（tmux 會重繪畫面）。
- 回滾：revert 即可；plist 的 fd 上限留著無害。
