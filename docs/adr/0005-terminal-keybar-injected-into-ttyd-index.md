# ADR 0005：終端機按鍵列以反代改寫 ttyd index 注入；按鍵經後端白名單 API 以 tmux send-keys 送出

- 狀態：已採用（2026-10-26）；捲動鍵與手指滑動見 ADR 0009
- 決策者：擁有者（加按鍵列、只送白名單鍵、沿用登入驗證）、architect（掛載方式與 API 形狀）

## 背景

手機開 `/term/?arg=<session>` 時，Claude 的權限框與選擇題要用方向鍵、Enter、Esc 操作，手機鍵盤沒有這些鍵。`/term/` 是反代 ttyd 自帶的 xterm.js 頁面，要在上面加一排按鍵。

## 決策

- **掛載**：反代 ttyd index（`GET /term/`）時，在第一個 `</head>` 前插入 `/keybar.css`、`/keybar.js`。index 請求拿掉 `Accept-Encoding`，讓 `http.Transport` 自己要 gzip 並透明解壓；改寫後若瀏覽器接受 gzip 再壓回去。找不到 `</head>` 原樣回傳並寫 log。
- **送鍵**：前端不經 xterm／ttyd WebSocket，改打 `POST /api/sessions/keys {"session","key"}`；後端 key 只收 13 個固定名稱並映射到 tmux 鍵名，session 先過名稱規則，再從當下 `list-sessions` 取完全同名者的 `$N`，執行 `send-keys -t $N <鍵名>`（argv、不經 shell、不用 `-l`）。
- **版面**：展開時以 CSS 把 `#terminal-container` 高度減去按鍵列高度，再呼叫 ttyd 掛在 `window.term` 上的 `fit()`。

## 考慮過的其他做法

- dev-console 自己出外框頁、iframe 載 ttyd：不依賴 ttyd 內部，但要把 `/term/` 的 `frame-ancestors 'none'`／`X-Frame-Options: DENY` 放寬成同源；焦點跨 frame（A4 量的 `document.activeElement` 在上層只會是 iframe）；iOS 對 iframe 高度有歷史怪癖。
- ttyd `-I` 自訂 index：要維護一整份 ttyd 前端 bundle 的副本，ttyd 升版最容易壞。
- 由前端把按鍵字元寫進 xterm（`term.input`／WebSocket）：會變成「任意輸入通道」，無法在伺服器端限制成白名單，也繞過寫入 API 的 CSRF 檢查。

## 取捨

- 依賴 ttyd 的三個點：`</head>`、`#terminal-container`、`window.term.fit`。前者不在就沒有按鍵列；後兩者不在時按鍵仍可送，只是終端機不會縮（最底幾行會被蓋）。ttyd 升版後要開手機視口看一次。
- ttyd 頁面沒有 viewport meta，手機上以約 980px 寬排版；不補 viewport（補了終端機欄數會從約 110 欄掉到約 45 欄，tmux `window-size latest` 會把 Mac 端也縮小），改由 keybar.js 依縮放倍率放大按鈕字級。
- 送到 session 目前作用中的 pane；同一個 session 開多個 pane 時只會送到作用中那個（與在終端機按鍵相同）。
- 回滾：revert 即可，無資料殘留。
