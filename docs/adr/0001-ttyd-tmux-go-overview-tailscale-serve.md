# ADR 0001：ttyd＋tmux＋Go 總覽頁＋tailscale serve；不做堡壘機、不自寫聊天 UI

- 狀態：已採用（2026-10-23）
- 決策者：擁有者（2026-10-18 規劃拍板），architect 實作時補細節

## 背景

要從手機與其他 Mac 看、操作這台 Mac 上多個 Claude／Codex profile 的 session，並知道誰在「等你允許／問你問題／做完」。只管一台機器、只有擁有者一個使用者，存取一律走 Tailscale。

## 考慮過的方案

1. **ttyd＋tmux＋Go 總覽頁＋tailscale serve（採用）**：tmux 當中樞，螢幕／SSH／網頁 attach 同一 session；ttyd 提供網頁終端機；Go 單一 binary 讀 tmux、hooks、transcript、state 組總覽；serve 給 HTTPS 與 tailnet 身分 header。
2. **JumpServer 類堡壘機**：有錄影、稽核、多使用者，但只管一台機器是殺雞用牛刀，MFA 已由 Tailscale 裝置金鑰＋帳號 MFA 取代，維運成本高。
3. **自寫聊天 UI（接 Claude Agent SDK／Codex app-server 結構化回覆）**：卡片上按允許、手機體驗最好，但等於重做官方 Remote Control；手機端改用官方 Remote Control（rc-<profile>）。

## 決策與理由

採方案 1。補充的實作決策：

- **ttyd 只開 unix socket（目錄 0700），由 Go 反代 `/term/`**：ttyd 沒有 TCP port，LAN 掃不到；身分檢查、Origin 檢查、session 名稱驗證集中在 Go 一處，ttyd wrapper 再驗一次名稱。
- **兩個 listener**：UI（7690，serve 反代）與 hook（7691，serve 不反代）。hook 端點因此從 tailnet 物理上打不到，不必靠 header 判斷來源。
- **身分**（已被 ADR 0002 取代：容器可冒充 loopback）：原本信任 serve 覆寫的 `Tailscale-User-Login` 與 `X-Forwarded-For`（取最右邊）。本機掛 tag 時 serve 不帶 login，只放行「來源就是本機 tailnet IP」；首頁靜態檔公開（無資料），讓本機 curl 健檢 200。
- **前端用 Go 內嵌的純 HTML/JS**，不用 Vue/Quasar：這是單機工具，一頁畫面、無建置步驟才能保持「clone → install」轉移；所有文字走 textContent 防 XSS。
- **即時推送用 SSE**（完整快照、內容沒變不推），不用 WebSocket：單向推送、瀏覽器自動重連、經 serve 不需特別設定；終端機的雙向流量已由 ttyd 的 WebSocket 負責。
- **standalone 名單內的 Codex profile 由 aiuse 自己建 tmux session（`AIUSE_TMUX_NO_ATTACH=1`）再認領**：aiuse 在 detached pane 內做 `switch-client` 時沒有自己的 client，依 tmux 找 client 的規則推論可能切走「最近使用的 client」（使用者桌面上的終端機；未實測、保守避開），所以加只建立不 attach 的開關；其他 profile 由 dev-console 直接建（2026-10-24 起由 ADR 0003 取代：所有 Codex profile 都交給 aiuse）。
- **rc-<profile> 用 `--spawn same-dir`、keeper 自動回答家目錄信任提示**：`--spawn worktree` 在非 git 的 `~` 直接報錯；家目錄信任每次都問且不保存，回 `y` 等同 使用者平常在家目錄開 claude 的確認。可用 `DC_RC_AUTO_TRUST=0` 關。
- **aiuse claude 預設進 tmux，`AIUSE_NO_TMUX=1` 逃生**；不在 claude session 設 `mouse on`，避免改變 iTerm 選字行為（Codex 照舊）。

## 影響與回滾

- 回滾：`./uninstall.sh`（停服務、移除 hooks、關 serve）＋ revert aiuse commit；ACL 規則到 admin console 刪。無資料遷移。
- tailnet 第一次用 serve 要啟用 HTTPS 憑證，機器名稱會進公開 Certificate Transparency 紀錄（不可逆），由擁有者手動啟用。
- 只亮跑在 tmux 裡的 claude；非 tmux 的 claude（`-p` 腳本）hook 會被略過。
