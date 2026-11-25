# ADR 0009：手機終端機捲動——後端依 pane 狀態送翻頁鍵、SGR 滾輪或 tmux copy-mode

- 狀態：已採用（2026-11-25）
- 決策者：擁有者（加 ▲／▼ 與手指滑動、不開 tmux mouse、不改 viewport、不加「到底」鍵與長按連捲、無慣性）、architect（捲動訊號選擇與 API 形狀）
- 延伸：ADR 0005（按鍵列與白名單送鍵）

## 背景

手機開 `/term/` 時 Claude／Codex 的畫面捲不動。量到的原因：

- Claude Code 與 Codex 都在 alternate screen（`alternate_on=1`），對話捲動由程式自己畫，tmux 的 `history_size` 近 0，copy-mode 捲不到。
- Claude Code 開了 SGR 滑鼠回報（`mouse_any_flag=1`、`mouse_sgr_flag=1`），桌機滾輪經 xterm.js 送 SGR 滾輪事件，所以桌機能捲；手機觸控在 xterm.js 不產生滾輪事件。
- Codex 0.170.0 沒開滑鼠回報（`mouse_any_flag=0`）；桌機滾輪在 Codex 上怎麼表現不在本次範圍，前端也不改滑鼠事件。

## 實測（隔離的 tmux server `-L`，2026-11-25，tmux 3.7c）

| 程式 | `PPage`／`NPage` | SGR 滾輪 `ESC[<64;x;yM`／`ESC[<65;x;yM` |
|---|---|---|
| Claude Code 2.1.332 | 有效，40 列畫面一次約 17 列 | 有效，一個事件約一列，輸入框無殘字 |
| Codex 0.170.0 | 有效，40 列畫面一次約 31 列（約一頁） | 有效（雖然沒開滑鼠回報，仍會解析），一個事件一列，輸入框無殘字 |
| 普通 shell | 會被 shell 當成按鍵 | 會被當成文字印出 |

方向鍵不能用來捲：Codex／Claude 在輸入框是空的時候按 `↑` 會叫出上一則輸入。

## 決策

- **新端點** `POST /api/sessions/scroll {"session","action","count"}`，不擴充 keys API。`action` 只收 `page-up`、`page-down`、`line-up`、`line-down`；翻頁 `count` 省略或 1，逐列 1～30（`tmux.MaxScrollLines`）。驗證順序與 keys 相同：CSRF → 動作與次數 → 名稱規則 → 當下 `list-sessions` 精確同名取 `$N`。
- **伺服器依 pane 當下旗標決定送什麼**（`tmux.PlanScroll`，純函式）：
  1. pane 在 tmux copy-mode：用 copy-mode 指令（`send-keys -X page-up|page-down`、`-X -N n scroll-up|scroll-down`），不送任何位元組給程式。在其他 tmux 模式（tree-mode 等）回 409。
  2. alternate screen：翻頁送 `PPage`／`NPage`；逐列送 SGR 滾輪序列 ×count，座標取 pane 中央。只有程式開了 SGR 滑鼠回報、或 `pane_current_command` 在已實測名單（目前只有 `codex`）時才送，否則回 409 不送——vim、less 這類程式收到同樣的位元組可能當成按鍵或文字。
  3. 一般畫面（shell）：往上捲且有 scrollback 時以 `copy-mode -e ; send-keys -X …` 同一次呼叫進入 copy-mode 並捲動，捲回底部自動離開；往下捲（已在底部）或沒有 scrollback 時不動作、回 204。
- **前端**：▲／▼ 送翻頁，走既有送出佇列。手指滑動在 capture 階段監聽 `#terminal-container` 的觸控事件：單指、垂直位移超過約 10 個裝置像素才算滑動（之前不 `preventDefault`，輕點照常聚焦）；之後以 xterm 實際列高（`.xterm-screen` 高 ÷ `term.rows`，CSS px）換算列數，與觸控座標同一個座標系，頁面縮放後仍是「手指移一列、內容移一列」；累積的列數在佇列空出來時合併成一個請求（一次最多 30 列）。偏橫向的拖動、多指一律放給瀏覽器；沒有慣性。滑鼠事件完全不處理。

## 考慮過的其他做法

- **tmux `mouse on`**：會改變 Mac 端 iTerm 的滑鼠行為（選字要按 Option），而且全螢幕程式仍沒有 scrollback，擁有者已排除。
- **前端把滾輪序列寫進 xterm／ttyd WebSocket**：變成任意輸入通道，伺服器無法限制內容、也繞過寫入 API 的 CSRF 檢查（同 ADR 0005 的理由）。
- **keys API 加任意字串或 `-l` 參數**：同上。
- **逐列也用 PageUp／PageDown**：手指滑動會一跳半頁到一頁，無法跟手。
- **一律送 SGR 滾輪、不看旗標**：shell 與沒開滑鼠回報的程式會收到亂碼。
- **shell 一律不處理**：手機上就看不到 shell 往上的輸出；copy-mode 是 tmux 自己的機制，不會送任何東西給 shell。

## 取捨

- 捲動位置在程式裡，手機捲上去後 Mac 端 attach 同一 session 也看到捲上去的畫面（擁有者接受）。shell 的 copy-mode 同樣是 pane 層級。
- shell 進 copy-mode 後，從終端機打的字會被 copy-mode 吃掉，要按 `Esc` 或捲到底離開。按鍵列其他 13 顆鍵的送法不變：pane 在 copy-mode 時，它們同樣交給 copy-mode 處理（`Esc` 即離開）。
- Codex 解析 SGR 滾輪是實測行為不是文件承諾；Codex 升版後要重測，失效時從 `sgrWheelCommands` 拿掉 `codex`，手指滑動會改成回 409，▲／▼ 仍可用。Claude Code 若哪天關掉滑鼠回報，逐列同樣會變成 409。
- 每列換算依 xterm 列高；手機上 ttyd 頁以約 980px 排版縮小顯示，字很小，手指滑一小段就會捲很多列——這是「內容跟手」的直接結果。
- 回滾：revert 即可，無資料殘留。
