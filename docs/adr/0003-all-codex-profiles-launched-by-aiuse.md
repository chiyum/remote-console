# ADR 0003：網頁開 Codex 一律交給 aiuse 建 session；不在 tmux 的既有 session 以唯讀卡片顯示

- 狀態：已採用（2026-10-24），取代 ADR 0001 中「其他 profile 由 dev-console 直接建」的部分
- 決策者：architect

## 背景

擁有者要求這台電腦的每個 Claude／Codex profile 都自動進 tmux，日後新增 profile 也不改程式。aiuse 已改成所有 Codex profile 的互動 TUI 都建 `aiuse-codex-<profile>-*`（standalone 名單內的 profile 標 `codex-v1`，其他標 `codex-direct-v1`；這是 aiuse 那邊的決定）。dev-console 原本只對 standalone 名單內的 profile 交給 aiuse，其他 profile 自建 `dc-codex-*`，等於同一種 session 有兩套命名與標記，Happy bridge 也只有 aiuse 那條路徑有。

## 決策

- 網頁開 Codex 一律走 `launchViaAiuse`：臨時 `dc-launch-<token>` pane 內跑 `aiuse codex <profile>`（帶 `AIUSE_TMUX_NO_ATTACH=<token>`）。aiuse 只在目前 session 名稱正好是 `dc-launch-<token>` 時才「建了不 attach」，使用者 shell export 的同名變數不會誤觸；建出的是 `new-session -d` 兄弟 session，不是巢狀。
- 認領條件（reviewer 第 1 回合退修）：新出現、profile 相符、標記對應該 profile（standalone 名單（DC_CODEX_STANDALONE_PROFILES）內的只認 `codex-v1`，其他只認 `codex-direct-v1`）、且帶 `@aiuse_ready=1`。aiuse 把 ready 放在所有標記、Happy identity 與 proof 之後最後寫。認領後再等 helper pane `pane_dead=1`（最多 10 秒）才刪 helper：原本一認領就刪，會 SIGHUP 還在寫 Happy option／`session.json` 的 aiuse，Happy helper 15 秒後 `path_timeout` 並關掉 app-server，TUI 失去 server。
- Claude 維持 dev-console 自建 `aiuse-claude-*`（標記與 aiuse 相同；pane 內 aiuse claude 看到 `TMUX` 直接跑）。
- 既有 `dc-codex-*` 卡片照常辨識為 codex，不再新建。

- 第 2 回合退修：helper 裡的 aiuse 若已結束（`pane_dead=1`）卻沒有 ready session，立刻帶畫面錯誤回報，不等滿 15 秒；aiuse 端帶了驗不過的 `AIUSE_TMUX_NO_ATTACH` 一律非 0 中止，不 attach、不 switch-client、也不在臨時 pane 裡直接跑 codex。
- profile 名稱規則與 aiuse 統一：`^[A-Za-z0-9_][A-Za-z0-9_-]*$`（首字不可是 `-`），大小寫照磁碟原樣；macOS 檔案系統不分大小寫，`validate.Profile` 另要求名稱是目錄裡原樣存在的那個（`ALPHA` 對 `alpha` 目錄會被拒）。standalone 名單內 profile 的認領以 aiuse 正規化後的小寫 profile 比對。

## 不在 tmux 的既有 session（A10，同日追加）

擁有者要求「包含你現在的 profile 也都要讓我看見」。已在跑、沒進 tmux 的程序無法搬進 tmux，所以只做唯讀卡片：

- 資料來源：程序表（`ps -axww -o pid,ppid,uid,tty,lstart,command`）為主。Claude 以 `<profile>/sessions/<pid>.json`（Claude Code 2.1.309 實測會寫，含 sessionId、cwd、startedAt、kind）對出 session id，並交叉比對 `CLAUDE_CONFIG_DIR`；Codex 沒有類似檔案，以 `CODEX_HOME` 判定 profile。不從 aiuse 命令列取 profile：Codex 的 direct 路徑會 exec 成 codex，命令列不含 profile。
- 未採：讀完整環境做更多判斷（含 token，違反資安要求）；改 hook 指令帶 `$PPID`（要重寫 7 個 profile 的 settings.json，且已在跑的 session 不一定重新載入 hooks）。
- hook 對應：沒有 `X-Tmux-Pane` 時以 payload 的 `session_id`（UUID 格式才收）為 key `sid:<id>`；token 檢查在前，不變。Prune 以程序表存活的 session id 為準，掃描失敗時保留。
- 卡片 `id` 為 `proc:<pid>`、沒有 `termUrl`，前端不畫任何連到終端機的按鈕。

## 取捨

- 命名、標記、Happy bridge 只在 aiuse 一處決定；新增 profile 只要建目錄。代價是開 Codex 要多一次臨時 pane 與最多 15 秒認領等待（與 standalone profile 原本相同）。
- 唯讀卡片每輪多一次 `ps`，新出現的候選各多一次 `ps -E`（之後走快取）；Claude 非 tmux session 無法以畫面檢查補「中斷」燈號。
- 回滾：revert 即回到自建 `dc-codex-*`、不列唯讀卡片；hooks-state.json 內殘留的 `sid:` 條目下一輪就被清掉。
