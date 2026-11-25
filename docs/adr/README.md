# ADR 索引

| 編號 | 標題 | 狀態 |
|---|---|---|
| [0001](0001-ttyd-tmux-go-overview-tailscale-serve.md) | ttyd＋tmux＋Go 總覽頁＋tailscale serve；不做堡壘機、不自寫聊天 UI | 已採用 |
| [0002](0002-loopback-is-not-identity-file-secret-cookie.md) | loopback 不是身分——改用檔案秘密（配對碼換 cookie、hook token） | 已採用（取代 0001 的身分段） |
| [0003](0003-all-codex-profiles-launched-by-aiuse.md) | 網頁開 Codex 一律交給 aiuse 建 session；不在 tmux 的既有 session 以唯讀卡片顯示 | 已採用（取代 0001 的其他 Codex profile 段） |
| [0004](0004-session-nickname-archive-own-file-keyed-by-claude-session-id.md) | session 暱稱與封存存在 dev-console 自己的檔案，Claude 以 session id 為 key；封存＝結束 tmux session | 已採用 |
| [0005](0005-terminal-keybar-injected-into-ttyd-index.md) | 終端機按鍵列以反代改寫 ttyd index 注入；按鍵經後端白名單 API 以 tmux send-keys 送出 | 已採用 |
| [0006](0006-ttyd-attach-no-exec-and-self-recovery-in-web-service.md) | ttyd-attach 不 exec tmux（修 macOS tty drain 死結漏 fd）；ttyd 自動恢復放 web 服務內，以反代追蹤的 WebSocket 數判斷有無連線 | 已採用 |
| [0007](0007-parameterized-optional-install.md) | 給別人安裝：選用模組、個人值改必填參數、只產檔模式（NO_LOAD／TARGET_HOME） | 已採用（認領協定部分由 0008 修訂） |
| [0008](0008-standalone-repo-dual-protocol-direct-mode.md) | 抽成獨立 repo；Codex 認領同時相容有／無 token 兩代 aiuse（同 profile 串行化）；沒有 aiuse 的直接模式 | 已採用 |
| [0009](0009-terminal-scroll-by-pane-state.md) | 手機終端機捲動：後端依 pane 狀態送翻頁鍵、SGR 滾輪或 tmux copy-mode；新增白名單捲動 API 與手指滑動 | 已採用（延伸 0005） |
