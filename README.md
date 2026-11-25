# 遠端工作台（remote-console）

[English](README.en.md)

從手機或另一台 Mac，經 Tailscale 開網頁看這台 Mac 上的 Claude／Codex session（tmux 畫面）、在網頁開新 session、看「等你允許／問你問題／做完」燈號、任務狀態與看門狗心跳。終端機永遠不出 tailnet。

- **只支援 macOS**（launchd、`ps -E`、Tailscale App 的行為都以 macOS 為準）。
- 服務與指令名稱沿用 `dev-console`（launchd label、資料目錄、binary 都叫這個名字）。
- 安裝與解除見下方「[安裝](#安裝)」與「[解除安裝](#解除安裝)」；有沒有 aiuse 的差別見「[兩種模式](#兩種模式)」。

## 架構

```
手機 / 其他電腦（Tailscale）
        │ https://<本機>.<tailnet>.ts.net/      ← tailscale serve（443，不開 funnel）
        ▼
127.0.0.1:7690  dev-console（Go 單一 binary，launchd 常駐）
        ├─ /                 總覽頁（內嵌靜態頁，無資料）
        ├─ /api/*            唯讀快照、SSE 即時推送、profile 清單、新開 session、暱稱／封存（需身分）
        └─ /term/?arg=<名稱>  反代 → ttyd（unix socket，launchd 常駐）→ ttyd-attach.sh → tmux attach；
                              index 頁注入按鍵列（ADR 0005）與連線守衛（ADR 0006）
127.0.0.1:7691  hook 接收端（只收本機 Claude hooks；serve 不反代此 port）

tmux 是中樞：Mac 螢幕（iTerm）、SSH、網頁三處 attach 同一個 session，window-size latest。
rc-<名稱>（選用，預設不裝）：tmux session 跑 `aiuse claude <profile> remote-control`（直接模式為 `claude remote-control`），launchd 保活，給 Claude App／claude.ai/code 用。
```

| 元件 | 位置 |
|---|---|
| binary 與輔助腳本 | `~/.local/share/dev-console/bin/` |
| ttyd socket（目錄 0700） | `~/.local/share/dev-console/run/ttyd.sock` |
| 安裝紀錄（0600；label 前綴、允許登入帳號、tailnet、常駐名稱、模式、目標家目錄、port；重跑安裝與解除時讀回，不在 repo 裡） | `~/.local/share/dev-console/install.env` |
| hook 燈號持久化 | `~/.local/share/dev-console/hooks-state.json` |
| session 暱稱與封存紀錄（0600，ADR 0004） | `~/.local/share/dev-console/session-meta.json` |
| 配對碼／已配對裝置雜湊／hook token 與 curl header 檔（皆 0600，目錄 0700） | `~/.local/share/dev-console/{pair-code.json,sessions.json,hook-token,hook-header}` |
| launchd | `~/Library/LaunchAgents/<DC_LABEL_PREFIX>.dev-console.{web,ttyd,rc-<名稱>}.plist` |
| log | `~/Library/Logs/dev-console/{web,ttyd,rc-<名稱>}.log` |

### 身分與防護（ADR 0002）

**loopback 不是身分**：App 版 Tailscale 的 serve 只能反代 TCP 127.0.0.1（實測 serve 到 unix socket 一律 502，連 /tmp 下 0666 的 socket 也是，屬 App 沙箱限制），而本機任何程序——包括 Docker 容器經 `host.docker.internal`——都能直連 127.0.0.1:7690 並偽造 `Host`、`X-Forwarded-For`、`Tailscale-User-Login`。所以放行只認「讀得到 0600 檔案的人」：

- **瀏覽器**：一次性配對碼以 **POST `/auth/pair`（必須帶相符 Origin）** 換 HttpOnly cookie（`dc_session`，SameSite=Strict，host-only）。經 serve：Secure、90 天；本機：12 小時。配對碼由 `dev-console pair` 寫進 0600 檔，10 分鐘、單次有效、錯 5 次作廢。
- **本機只認 `http://dev-console.localhost:7690`**（`*.localhost` 由瀏覽器與 curl 解析到 loopback）。`127.0.0.1`／`localhost` 不再發 cookie 也不認 cookie：cookie 不分 port，綁在 127.0.0.1 會被瀏覽器送到同機其他服務（例如容器開的 `127.0.0.1:8080`），被重放就等於繞過身分。
- **hook**：每次帶 `X-DC-Hook-Token`，由 `curl -H @~/.local/share/dev-console/hook-header`（0600）讀入，token 不出現在 `ps` 的命令列。
- header 只用來**拒絕**：Host 必須是 tailnet 名稱或 `dev-console.localhost:7690`（擋 DNS rebinding）；帶了 `Tailscale-User-Login` 卻不在 `DC_ALLOWED_LOGINS`（安裝時必填，沒有預設帳號）就拒絕。
- 服務只綁 127.0.0.1；ttyd 不開 TCP port（只開 unix socket，只有 dev-console 連得到）。LAN 與公網都連不到。
- 首頁 `/`、`/healthz`、`/favicon.*`、`POST /auth/pair` 公開（首頁不含資料）；其餘都要 cookie。
- 寫入 API（新開 session、暱稱、封存、取消封存、終端機按鍵）：需 cookie、`Content-Type: application/json`、`X-Dev-Console: 1`、相符的 `Origin`（一律必帶，本機直連也不豁免）。終端機 WebSocket 也檢查 Origin（防跨站 WebSocket 劫持）。
- CSP：總覽頁 `script-src 'self'`（無 inline）；`/term/` 因 ttyd 頁面是內嵌 script 的單檔，放寬 `'unsafe-inline'`（注入的按鍵列是同源外部檔，不需另外放寬）。兩者都 `frame-ancestors 'none'`。ttyd 的 `Server` header 由反代移除。
- session 名稱（`?arg=`）雙層驗證：Go 端（格式＋存在）與 `ttyd-attach.sh`（格式＋存在，`=` 精確比對）。
- 新開 session 的 profile（aiuse 模式）只接受 `~/.ai-profiles/{claude,codex}/` 下實際存在、名稱符合 `^[A-Za-z0-9_][A-Za-z0-9_-]*$`、且與目錄原樣相同的目錄；直接模式不收任何 profile 值（帶了就 400）。兩種模式的目錄都要 realpath 後在 `$HOME` 之下且存在；都以 exec 陣列呼叫 tmux，不經 shell。

## 安裝

### 前提

- Mac（**只支援 macOS**），已裝 [Homebrew](https://brew.sh)。
- [Tailscale](https://tailscale.com) App 已登入；要用的手機或另一台電腦也登入同一個 tailnet。
- tmux 與 ttyd（install.sh 用 brew 自動安裝）、`go`、`jq`（`brew install go jq`）。終端機捲動需要 tmux **3.0a 以上**（用到 `#{&&:}`／`#{||:}` 格式運算子〔2.6 起〕與 `send-keys -H`〔CHANGES 記在 3.0，原始碼 tag 3.0a 起確認有〕；本機開發用 3.7c），舊版的症狀見「終端機按鍵列」的捲動段。
- Claude Code（`claude`）與／或 Codex（`codex`）至少一個。
- 選用：[aiuse](#兩種模式)（多帳號切換工具，不在本 repo）。沒有它也能裝，會是「直接模式」。

### 安裝時要回答的三題

| 題目 | 對應參數 | 怎麼填 |
|---|---|---|
| 1. 你的 Tailscale 登入帳號與 tailnet 名稱 | `DC_ALLOWED_LOGINS`、`DC_TAILNET` | `DC_ALLOWED_LOGINS` 填允許從手機登入的 Tailscale 帳號 email（多個用逗號分隔；**必填，沒有預設帳號**）。`DC_TAILNET` 填網址 `<機器>.<名稱>.ts.net` 裡的 `<名稱>`（Tailscale App 或 `tailscale status --json` 的 `MagicDNSSuffix` 可查） |
| 2. 服務名稱前綴 | `DC_LABEL_PREFIX` | 反向網域格式，例如 `com.example.remote`。實際 launchd label 是 `<前綴>.dev-console.{web,ttyd,rc-<名稱>}`；解除時只刪這一組 |
| 3. 要不要常駐手機遙控（Claude remote-control） | `DC_RC_PROFILE` | 不要就不設。要的話：有 aiuse 填要常駐的 Claude profile 名稱；直接模式填任一名稱（例如 `main`，只用來命名服務與 tmux session，實際以預設帳號執行 `claude remote-control`） |

### 步驟

```bash
git clone https://github.com/chiyum/remote-console.git
cd remote-console

# 1) 先試裝到隔離目錄（不啟動任何服務、不呼叫 tailscale／launchctl／brew，產物都落在 /tmp/rc-try 底下）
TARGET_HOME=/tmp/rc-try NO_LOAD=1 \
DC_LABEL_PREFIX=com.example.remote \
DC_ALLOWED_LOGINS=you@example.com \
DC_TAILNET=example-tailnet \
./install.sh
TARGET_HOME=/tmp/rc-try ./uninstall.sh       # 試裝看完就清掉

# 2) 正式安裝（冪等，可重跑；內容沒變就不重載）
DC_LABEL_PREFIX=com.example.remote \
DC_ALLOWED_LOGINS=you@example.com \
DC_TAILNET=example-tailnet \
./install.sh                                   # 要常駐手機遙控再加 DC_RC_PROFILE=<名稱>

# 3) 每台瀏覽器第一次要配對
~/.local/share/dev-console/bin/dev-console pair
```

install.sh 會：brew 裝 tmux／ttyd、go build、判斷模式並印出（aiuse 模式／直接模式，直接模式會列出找到的 `claude`／`codex`）、安裝 binary 與腳本（tmp + mv 原子替換）、產生並載入 launchd plist、寫 `install.env`（0600）、把 hooks 合併進 `~/.claude/settings.json` 與每個 Claude profile 的 settings.json（settings.json 是 symlink 的略過）、檢查 tmux `window-size`、設定 `tailscale serve`。

- **重跑與升級**：`git pull` 後直接 `./install.sh`。只有 `DC_LABEL_PREFIX`、`DC_ALLOWED_LOGINS`、`DC_TAILNET`、`DC_RC_PROFILE` 四個沒給時會從 `install.env` 讀回（明確設成空值＝清空，例如 `DC_RC_PROFILE=` 取消常駐；`DC_ALLOWED_LOGINS=` 會被擋下）；其餘參數（`DC_UI_PORT`／`DC_HOOK_PORT`、`DC_TTYD_*`、`DC_AIUSE_PATH`、`DC_CLAUDE_BIN`／`DC_CODEX_BIN`、`DC_TS_HOSTNAME`）不會記住，改過預設值的話重跑時要再給一次。資料目錄裡既有的配對、暱稱、燈號、hook-token 不會被覆寫，既有 tmux session 不會被關；serve 已指向本服務時不重設。
- **只產檔不啟動**：加 `NO_LOAD=1`（可再加 `TARGET_HOME=<目錄>` 指定目標家目錄），只寫出 binary、腳本、plist、install.env 與 hooks，不執行 `launchctl`、不呼叫 `tailscale`、不動 tmux server、不呼叫 `brew`（Homebrew 路徑用 `BREW_PREFIX` 或依 CPU 推算 `/opt/homebrew`／`/usr/local`）；`TARGET_HOME` 不是本機家目錄時，go build 的快取（`GOCACHE`／`GOPATH`／`GOMODCACHE`）放在目標家目錄底下的 `Library/Caches/go-build`、`go/`，不寫進真實家目錄；主機名用 `DC_TS_HOSTNAME`（預設本機 LocalHostName）＋`DC_TAILNET` 組成。之後在本機家目錄拿掉 `NO_LOAD` 重跑即正式啟用。
- 第一次在某個 tailnet 用 serve 時，install.sh 會印出「Serve is not enabled on your tailnet」與啟用網址（不會卡住）。到網址按啟用後重跑 install.sh。啟用 HTTPS 憑證會讓機器名稱出現在公開的 Certificate Transparency 紀錄。
- 443 的根路徑 `/` 已被你別的服務用掉時，install.sh 會在動任何服務前停下，不覆寫你的設定。

| 參數 | 必填 | 預設 | 用途 |
|---|---|---|---|
| `DC_LABEL_PREFIX` | 是 | — | launchd label 前綴（反向網域格式）；實際 label 為 `<前綴>.dev-console.{web,ttyd,rc-<名稱>}`，解除時只刪這組 |
| `DC_ALLOWED_LOGINS` | 是 | — | 允許從 tailnet 登入的 Tailscale 帳號 email，逗號分隔 |
| `DC_TAILNET` | 是 | — | `<名稱>.ts.net` 的 `<名稱>`；正常模式與 `tailscale status` 比對，不符就中止 |
| `TARGET_HOME` | 否 | `$HOME` | 目標家目錄，所有產出都落在這底下 |
| `NO_LOAD` | 否 | `0` | `1`＝只產檔不啟動 |
| `DC_TS_HOSTNAME` | 否 | tailscale 回報值／LocalHostName | tailnet 上的本機名稱 |
| `DC_UI_PORT`／`DC_HOOK_PORT` | 否 | `7690`／`7691` | 總覽頁與 hook 接收 port（只綁 127.0.0.1） |
| `DC_RC_PROFILE` | 否 | 空 | 設了才裝手機遙控常駐 `rc-<名稱>`（見上方第 3 題） |
| `DC_AIUSE_PATH` | 否 | `<TARGET_HOME>/.local/bin/aiuse` | aiuse 位置；不存在或不可執行＝直接模式 |
| `DC_CLAUDE_BIN`／`DC_CODEX_BIN` | 否 | 從 session PATH 找 | 直接模式執行的 `claude`／`codex` 絕對路徑 |
| `DC_TTYD_*` | 否 | 見「ttyd 健康與自動恢復」 | ttyd fd 上限與自動重啟 |

### 從舊版升級（dev-console 或內嵌在其他套件裡的版本）

用**同一個 label 前綴**在新 repo 原地重跑 `install.sh` 即可：launchd label、資料目錄 `~/.local/share/dev-console`、tailscale serve 目標都相同，已配對的裝置、暱稱與封存紀錄、hook-token 保留，既有 tmux session 不受影響；web、ttyd 與常駐遙控服務會重啟一次（幾秒）。

```bash
cd remote-console
DC_LABEL_PREFIX=<舊版用的前綴> DC_ALLOWED_LOGINS=<你的帳號> DC_TAILNET=<你的 tailnet> \
DC_RC_PROFILE=<舊版常駐的名稱，沒有就不給> ./install.sh
```

- **不要先跑舊版的 uninstall.sh**：它會刪掉整個資料目錄（包含配對紀錄，所有瀏覽器都要重新配對），並移除 443 上的 tailscale serve 設定。
- 前綴要和舊版相同（看 `ls ~/Library/LaunchAgents | grep dev-console`）；填錯前綴會裝出第二套服務、和舊的搶同一個 port。
- 舊版沒有 `install.env`，第一次重跑時三個必填參數要給齊；之後就會記住。

### 兩種模式

模式在每次網頁新開 session 時判斷（之後才裝或移除 aiuse，不必重裝）：`DC_AIUSE_PATH`（預設 `~/.local/bin/aiuse`）是存在、可執行的檔案＝**aiuse 模式**，否則＝**直接模式**。總覽、終端機、燈號、暱稱／封存、任務在兩種模式都一樣。

| | aiuse 模式 | 直接模式 |
|---|---|---|
| 網頁新開 session | 選 Claude／Codex、選 profile（`~/.ai-profiles/{claude,codex}/` 底下的目錄）、選目錄 | 選 Claude／Codex、選目錄；**沒有 profile 選單** |
| 實際執行 | `aiuse claude <profile>`；Codex 交給 aiuse 自己建 session 再認領 | `claude`／`codex`（由 session PATH 解析成絕對路徑，或 `DC_CLAUDE_BIN`／`DC_CODEX_BIN`），以 exec 陣列交給 tmux，不經 shell |
| session 名稱 | `aiuse-claude-<profile>-<UTC>-<hex>`、`aiuse-codex-<profile>-*` | `dc-claude-<UTC>-<hex>`、`dc-codex-<UTC>-<hex>` |
| 找不到指令 | aiuse 回報的錯誤 | 回「找不到 claude／codex 指令」，不建立任何 tmux session |
| 手機遙控常駐 | `aiuse claude <profile> remote-control` | `claude remote-control`（預設帳號）；正式安裝時找不到 `claude` 會直接中止，不會裝出一直失敗的常駐服務 |
| 燈號 | 每個 profile 與 `~/.claude` 的 settings.json 都有 hook | `~/.claude/settings.json` 的 hook |

**直接模式的已知限制**（刻意不做）：

- 不在 tmux 裡（例如 iTerm 直接開）的 `claude`／`codex` **不會顯示**在總覽頁；唯讀卡片只認得 aiuse profile 開的 session。
- 卡片**不顯示對話內容**（最後回覆、tool、token）；燈號照常。
- 沒有多帳號：一律用 `claude`／`codex` 的預設帳號（`~/.claude`、`~/.codex`）。

### Tailscale ACL（本機掛 tag 時才需要）

本機掛 tag（例如 `tag:workstation`）後不屬於 `autogroup:self`，手機預設連不到。維持一條最小規則，來源只給擁有者本人：

```bash
TS_API_TOKEN=<Admin API token> scripts/tailscale-acl.sh --src you@example.com                       # 你的帳號 → 本機 tailnet IPv4:22,443
TS_API_TOKEN=... scripts/tailscale-acl.sh --src you@example.com --ip 100.x.y.z --dry-run   # 先看差異與 validate 結果
```

流程固定是 GET（取 ETag）→ 同目的地的其他 accept 規則（例如舊的 `autogroup:member`）替換成目標規則、已是目標狀態就跳過 → `/acl/validate` → 帶 `If-Match` POST。policy 含註解時會中止（JSON 往返會吃掉註解）。token 只從環境變數讀。

### 配對裝置（每台瀏覽器第一次）

```bash
~/.local/share/dev-console/bin/dev-console pair        # 印出配對碼與兩個網址：https://<tailnet>/?pair=<碼>、http://dev-console.localhost:7690/?pair=<碼>
~/.local/share/dev-console/bin/dev-console devices     # 列出已配對裝置
~/.local/share/dev-console/bin/dev-console revoke-all  # 撤銷全部（執行中的服務依檔案 mtime 立即生效）
```

手機：開總覽頁 → 「配對此裝置」輸入配對碼，或直接開印出的 `/?pair=<碼>` 網址（頁面會自動 POST 兌換並把碼從網址列拿掉）。cookie 是 host-only：本機名稱與 tailnet 網址各自配對一次。

## 本機驗收（QA／Playwright）

本機網址一律用 **`http://dev-console.localhost:7690/`**（不是 127.0.0.1，127.0.0.1 不認 cookie）。

1. 每個瀏覽器 context 先取碼：`CODE=$(~/.local/share/dev-console/bin/dev-console pair | grep -oE '[A-Z0-9]{5}-[A-Z0-9]{5}' | head -1)`。
2. Playwright：`page.goto("http://dev-console.localhost:7690/?pair=" + CODE)`，頁面自動 POST 兌換、拿到 cookie、網址列變回 `/`；或在首頁 `#pair-code` 填碼後按「配對」。之後同一 context 的 `/`、`/term/?arg=<名稱>`、API 都可用（本機 cookie 12 小時）。
3. 配對碼單次有效，每開一個新 context（或換到 tailnet 網址）要重新 `pair`。驗完可 `dev-console revoke-all`。
4. curl（7.78 起把 `*.localhost` 解析成 loopback）：
   `curl -c jar -H 'Origin: http://dev-console.localhost:7690' --data-urlencode "code=$CODE" http://dev-console.localhost:7690/auth/pair`（回 204），之後以 `-b jar` 帶 cookie；寫入 API 另需 `-H 'Origin: http://dev-console.localhost:7690' -H 'X-Dev-Console: 1' -H 'Content-Type: application/json'`。
5. 本機 Playwright 若找不到它自帶的瀏覽器版本，用 `chromium.launch({ channel: "chrome" })` 改用已安裝的 Chrome。

## 使用

> **與 aiuse 的相依**（只有 aiuse 模式）：總覽、終端機、燈號、暱稱／封存只需要 tmux 與 Claude hooks。網頁「新開 session」中，Claude 由 dev-console 自建 tmux session 再在裡面跑 `aiuse claude <profile>`，任何版本的 aiuse 都可用；Codex 則交給 aiuse 自己建 tmux session，需要 aiuse 支援 `AIUSE_TMUX_NO_ATTACH` 與 `@aiuse_managed`／`@aiuse_ready` 標記。認領支援兩代協定（ADR 0008）：寫 `@aiuse_launch_token` 的新版，以及不寫 token、以 `codex-direct-v1` 或 `codex-v1` 標記的舊版。沒有這些功能的 aiuse 會在 15 秒後回報「aiuse 未建立 codex session」，其他功能不受影響。下方「逃生開關」中 aiuse 自動進 tmux 的各列，也只在 aiuse 有 tmux 功能時適用。

- 總覽頁：`https://<本機>.<tailnet>.ts.net/`
- 終端機：`https://<本機>.<tailnet>.ts.net/term/?arg=<tmux session 名稱>`（總覽頁卡片的「開終端機」）；底部有按鍵列，見下節
- 新開 session：總覽頁「＋ 新開 session」→ 選 Claude／Codex（aiuse 模式再選 profile）→ 開始，會自動跳到終端機。目錄預設 `~`，「指定目錄」是收起的進階選項（接受 `~/...` 或絕對路徑，必須在 home 之下）。
  - 直接模式：session 名 `dc-claude-<UTC>-<hex>`／`dc-codex-<UTC>-<hex>`，標 `@dc_created=1`、不寫任何 `@aiuse_*` 標記（見「兩種模式」）。
  - Claude（aiuse 模式）：session 名 `aiuse-claude-<profile>-<UTC>-<hex>`，標記與 aiuse 自己開的相同，`aiuse sessions list` 看得到。
  - Codex（aiuse 模式，所有 profile，含日後新增）：在臨時 `dc-launch-<token>` session 裡跑 aiuse（帶 `AIUSE_TMUX_NO_ATTACH=<token>`，aiuse 只在名稱與 token 相符時才「建了不 attach」），由 aiuse 自己建 `aiuse-codex-<profile>-*` session，再認領它（ADR 0008）：
    - 只看本次新出現（建立前不存在）、已有 `@aiuse_ready=1`、標記為 `codex-direct-v1`（profile 完全相同）或 `codex-v1`（aiuse 會把 profile 轉小寫，比對不分大小寫）的 session。哪個 profile 走哪種標記由 aiuse 決定，dev-console 不維護 profile 名單。
    - 帶 `@aiuse_launch_token` 的必須等於本次 token（不符＝別次新開，絕不認領），相符就立刻認領。
    - 不帶 token 的（舊版 aiuse）只在臨時 pane 裡的 aiuse 以 exit 0 結束後才接受，而且必須唯一；同時出現兩個以上就回錯誤，不猜。
    - 同一個 profile 的 Codex 新開會排隊一個一個做（不分大小寫），所以網頁同時開兩個同 profile 的 Codex，後一個的「建立前」一定已含前一個的 session，各自只認領到自己的。
    - 認領後等臨時 pane 裡的 aiuse 結束才刪掉臨時 session（臨時 pane 是 aiuse 的終端機，太早刪會用 SIGHUP 打斷還沒退出的 aiuse；不讓 dev-console 依賴「aiuse 標 ready 後沒有其他步驟」）。
  - profile 選單即 `~/.ai-profiles/{claude,codex}/` 底下名稱合法（英數、底線、連字號，首字不可是 `-`）的目錄，`--help`、`shared` 不列；新增目錄重整頁面就出現，不必改程式或重啟。
- Mac 本機接手：`tmux attach -t <名稱>` 或 `aiuse sessions attach <名稱>`。
- 另一台 Mac：`ssh <使用者>@<本機 tailnet IP> -t tmux attach -t <名稱>`（App 版 Tailscale 不能當 SSH server，走 macOS 內建 sshd；系統設定 → 一般 → 共享 → 遠端登入 要開）。

### 終端機按鍵列（手機操作 Claude 選單，ADR 0005）

手機鍵盤沒有方向鍵、Esc、Tab，Claude 的權限框與選擇題沒辦法操作。終端機頁底部有一排按鍵：`Esc`、`Tab`、`⇧Tab`、`↑ ↓ ← →`、`Enter`、`1 2 3 4`、`Ctrl+C`，點了等於在這個 session 按下該鍵；列尾另有 `▲ 上捲`、`▼ 下捲`（ADR 0009）。

- **手機用法**：權限框出現時點 `↓`／`↑` 移動、`Enter` 確認，或直接點選項編號 `1`～`4`；`Esc` 取消；`⇧Tab` 切換 Claude 模式。`Ctrl+C` 會先跳確認框，取消就不送。
- **顯示**：觸控裝置（`pointer: coarse`）預設展開，桌機預設收起，右下角「⌨ 按鍵」展開、列尾「收起」收起；展開狀態存在 localStorage（觸控與桌機分開記，存不了就用預設）。
- **不會彈出手機鍵盤**：按鈕不取得焦點，點之前若焦點在 xterm 的輸入框會先移開（正在用手機鍵盤打字時點按鍵列，鍵盤會收起）。
- **不遮畫面**：展開時終端機區縮成「視窗高 − 按鍵列高」再重新 fit，xterm 列數變少、tmux 視窗跟著變小，最底一行不被蓋住。ttyd 頁沒有 viewport meta，手機上以約 980px 寬排版再縮小，按鍵依縮放倍率放大到手指點得到的大小（沒有改 viewport，終端機欄數與以前相同）。
- **順序**：連點時前端排隊，前一個請求結束才送下一個，順序與次數不變（捲動鍵與手指滑動也排在同一條佇列）。
- **捲動（ADR 0009）**：Claude 與 Codex 是全螢幕程式，捲動由程式自己處理，tmux 沒有可捲的歷史，手機觸控也不會產生滾輪，所以以前在手機上捲不動。
  - `▲ 上捲`／`▼ 下捲`：每點一次翻一頁（送程式的 PageUp／PageDown；Claude Code 2.1.332 在 40 列畫面約捲 17 列，Codex 0.170.0 約捲一整頁）。
  - 手指在終端機上下滑：方向和一般手機 App 相同，手指往下拖＝看更早的內容；手指移動一列字高就捲一列（換算用 xterm 實際的列高，頁面縮放後照樣跟手）。位移小於約 10 個裝置像素視為輕點，照常聚焦、叫出鍵盤；雙指縮放與偏橫向的拖動不處理；沒有慣性，手指放開後只把已滑過的列數送完。只有觸控事件會觸發，桌機滑鼠滾輪與拖曳選字不變。
  - 捲到底：用 `▼` 或手指往上滑回去。
  - 捲動位置是程式自己記的：手機捲上去後，Mac 端 attach 同一個 session 也會看到同樣的畫面。
  - 普通 shell（不是全螢幕程式）：往上捲時進入 tmux copy-mode（`copy-mode -e`），捲回底部自動離開；在 copy-mode 中打字會被 copy-mode 吃掉，按 `Esc`（tmux 預設 emacs 鍵位）或捲到底即可離開。在底部往下捲不做任何事。
  - 捲動回 409（畫面不支援或剛改變）時，這次手指滑動就停止送出，錯誤只顯示一次；手指放開再滑一次即可。
  - tmux 版本太舊（未實測，依原始碼推論）：2.6 以前不認得 `#{&&:}`／`#{||:}`，條件展開成空字串、永遠不成立，**每次捲動都回 409「畫面剛改變」**、什麼都不送；2.6～3.0 不支援 `send-keys -H`，翻頁與 shell 捲動正常，但手指滑動（逐列）會回 500「捲動失敗」，log 有 tmux 的錯誤訊息。遇到請升級 tmux（`brew upgrade tmux`）。
  - 其他沒開滑鼠回報的全螢幕程式（例如 vim、less）：`▲`／`▼` 送 PageUp／PageDown；手指滑動不送（回 409、按鍵列顯示「捲動沒送出」），避免把滾輪序列當成文字塞進程式。
- **掛載方式**：dev-console 反代 ttyd 的 index 頁時，在 `</head>` 前插入 `/keybar.css`、`/keybar.js`（靜態檔，不含資料）。瀏覽器接受 gzip 時，反代讓 Go 的 Transport 透明解壓、改寫後再壓回去。找不到 `</head>`（ttyd 改版）就原樣回傳並寫 log：終端機照常可用，只是沒有按鍵列。`/term/?arg=` 網址不變。

| API | body | 說明 |
|---|---|---|
| `POST /api/sessions/keys` | `{"session":"aiuse-claude-work-…","key":"down"}` | `key` 只收 `esc`、`tab`、`btab`、`up`、`down`、`left`、`right`、`enter`、`1`～`4`、`ctrl-c`（對應 tmux 鍵名 `Escape`、`Tab`、`BTab`、`Up`…、`C-c`），其他一律 400，不送任意文字。`session` 必須符合名稱規則（含 `;`、`:`、`=` 等 400），並在當下的 `list-sessions` 有**完全同名**的 session（否則 404，不會前綴比對到別的 session）；以該 session 的 `$N` 執行 `tmux send-keys -t $N <鍵名>`，送到它目前作用中的 pane。成功 204；回應不帶任何 tmux 輸出。需 cookie＋寫入 API 標頭（缺任一 403） |
| `POST /api/sessions/scroll` | `{"session":"…","action":"page-up"}`、`{"session":"…","action":"line-up","count":3}` | `action` 只收 `page-up`、`page-down`（按鍵列；`count` 省略或 1）與 `line-up`、`line-down`（手指滑動；`count` 必填，1～30 的整數），其他、0、負數、小數、超過 30 一律 400。`session` 驗證與目標規則同上。後端先以 `display-message` 讀 pane 的 `alternate_on`、`mouse_any_flag`、`mouse_sgr_flag`、`pane_in_mode`、`history_size` 等旗標再決定送什麼：全螢幕程式翻頁送 `PPage`／`NPage`，逐列送 SGR 滾輪序列 `ESC[<64;x;yM`（上）／`ESC[<65;x;yM`（下）×count（座標取 pane 中央，序列由伺服器常數組成，只在程式開了 SGR 滑鼠回報、或是已實測的 `codex` 時才送）；普通 shell 往上用 `copy-mode -e`＋`send-keys -X`；tmux copy-mode 中直接用 copy-mode 的捲動指令。真正送出是一次 `if-shell -F` 呼叫：tmux server 在同一個指令內重新確認作用中 pane 與旗標沒變才執行，變了就不送、回 409「畫面剛改變」。不支援時也回 409 且不送任何東西。成功 204 |

### 暱稱與封存（ADR 0004）

- **暱稱**：卡片標題旁的 ✎。有暱稱時暱稱是主標題、tmux 名稱縮成小字；留空＝清除。最多 40 字，前後空白去掉，控制字元與格式字元（換行、ESC、U+202E 等）會被拿掉，結尾不可是 `;`（tmux 以 argv 執行時會把結尾分號當指令分隔）。前端一律以 textContent 顯示。
- **key**：Claude 已知 session id 時暱稱只存 `claude:<session id>`（tmux 卡片取 hook 的 session id，還沒有 hook 時讀 `<profile>/sessions/<pid>.json`），用接手指令 `--resume` 重開後自動帶回；同一個 tmux 裡 `/resume` 切到別段對話，顯示的是那段對話自己的暱稱。session id 還不知道時存在 `tmux:<名稱>`，之後第一次對上 session id 且該對話沒有暱稱紀錄時搬過去一次（搬完刪掉）。Codex 與普通 shell 以 tmux 名稱存，Codex 重開不會帶回。
- **寫檔**：先寫好新檔（tmp＋rename）才換掉記憶體裡的表；寫檔失敗時 API 回 500，畫面與檔案都維持原狀。
- **iTerm2 tab 標題**：有暱稱的 tmux session 會被設 session 層 `set-titles on`、`set-titles-string '#{@dc_nickname}'` 與 `@dc_nickname`，attach 中的 iTerm2 tab 約 1 秒內變成「暱稱 (tmux)」（括號是 iTerm2 自己加的工作名稱）；暱稱裡的 `#(...)`／`#{...}` 不會被 tmux 展開。清空時先送空標題（iTerm2 回到預設名稱，例如 `tmux`），下一輪再 unset 回全域預設。沒設過暱稱的 session 完全不動，`~/.tmux.conf` 也不動。`--resume` 回來被認出有暱稱時會補設。
- **封存**：卡片的「封存」，確認後：
  - aiuse 開的 tmux session（有 `@aiuse_managed` 與 profile）：結束 tmux session，可用已封存區的接手指令找回（Claude `aiuse claude <profile> --resume <session id>`；session id 未知時 `--resume` 讓 Claude 列清單；Codex `aiuse codex <profile> resume`）。
  - 普通 shell 等其他 tmux session（含 `rc-<名稱>`，rc-keeper 會自己重建）：結束 session，**無法找回**。
  - 不在 tmux 的唯讀卡片：只隱藏，不結束程序（dev-console 不會對該 pid 送任何訊號）；程序結束後紀錄仍留在已封存區。已封存區標「只隱藏（程序仍在執行）」，程序結束後改標「程序已結束」（每輪快照即時判定，程序表查詢失敗時維持前者）。
  - **隱藏規則**：只有唯讀卡片會因封存而隱藏（同一個程序還活著時）。tmux 卡片封存時已被結束，封存紀錄（key `tmux:<名稱>@<封存時間>`）只是已封存區的歷史；之後同名新開的 session 照常顯示，同名 session 可以封存很多次、各自一筆。
  - **自動移除（A10）**：同一個 Claude session id 以新的活著的卡片重新出現（任何 tmux 卡片，或 pid 不同於封存時的唯讀卡片）時，該封存紀錄自動移除。封存時那個程序本身還活著、或同名 tmux session 還沒被結束完，都不算重開。Codex 沒有 session id，不適用。
- **已封存（N）**：總覽頁最下方，預設收起。每筆有暱稱、工具、profile、工作目錄、封存時間、接手指令（「複製」）與「取消封存」。取消封存只移除紀錄，不會重開 session。

| API | body | 說明 |
|---|---|---|
| `POST /api/sessions/nickname` | `{"id":"$12","nickname":"客服 bug"}` | `id` 是卡片 id（`$N` 或 `proc:<pid>`），必須出現在當下的 tmux／程序表；空字串＝清除。400 格式錯、404 已不存在 |
| `POST /api/sessions/archive` | `{"id":"$12"}` | 回封存紀錄。tmux 卡片先寫紀錄再 `kill-session -t $N`，kill 失敗只撤回這一筆並回 500；紀錄寫不進去回 500 且不 kill；唯讀卡片已封存過回 409 |
| `POST /api/sessions/unarchive` | `{"key":"tmux:<名稱>@<ns>"}` | 只移除紀錄；不存在回 404，成功 204 |

快照（`/api/snapshot`、SSE）每張卡片多 `nickname`、`metaKey`（`tmux:<名稱>`／`proc:<pid>:<啟動秒>`）、`resumeCommand`；頂層多 `archived`（最新封存在前）。

### 不在 tmux 的既有 session（唯讀卡片，ADR 0003）

已在 iTerm 等終端機直接跑、沒有進 tmux 的 aiuse Claude／Codex 互動 session，程序沒辦法搬進 tmux，總覽頁以虛線框的唯讀卡片顯示：profile、工具、tty、PID、啟動時間、工作目錄（Claude），標「不在 tmux（只能看，不能從網頁操作）」，附接手方式（在原終端機結束後 `aiuse claude <p> --resume <session id>`／`aiuse codex <p> resume`，重開就會進 tmux；指令由後端 `resumeCommand` 組，與封存紀錄共用）。沒有「開終端機」按鈕，可改暱稱、可封存（只隱藏）。

- 判定（寧可不顯示也不誤標）：程序是自己的、有 tty、tty 不屬於任何 tmux pane（`#{pane_tty}`）、祖先不是另一個候選（session 內 shell 工具跑的子程序不重複列）。Claude 要同時有 `~/.ai-profiles/claude/<p>/sessions/<pid>.json`（`kind=interactive`、pid 與啟動時間對得上 ps，防 pid 重用）且程序的 `CLAUDE_CONFIG_DIR` 正好是該 profile 目錄；Codex 要 `CODEX_HOME` 正好是 `~/.ai-profiles/codex/<p>`（目錄存在、名稱合法）且 argv 是互動 TUI（exec、app-server 等子命令不列）。判定不出 profile 就不顯示。
- 資安：`ps -E` 只對候選程序執行，只抽 `CLAUDE_CONFIG_DIR`／`CODEX_HOME` 兩個 key，原始輸出不保存；快取只存「pid＋啟動時間 → profile」，程序結束即清。API 只回 profile、tool、tty、pid、啟動時間、cwd、Claude session id。
- 燈號與最後回覆：Claude 以 hook 的 `session_id` 對應（見下節）；最後回覆讀 `projects/<cwd 轉換>/<session id>.jsonl`。這類 session 沒有畫面可抓，工作中按 Esc 中斷不會自動熄燈。Codex 沒有 hooks，只顯示基本資訊。
- 消失時間：每 2 秒掃一次程序表，程序結束後下一輪就消失；掃描失敗或 tmux 查詢失敗的那一輪不列唯讀卡片、也不清燈號。

### 燈號（Claude hooks）

`~/.claude/settings.json` 與每個 Claude profile 的 settings.json 追加 `UserPromptSubmit／PreToolUse／PermissionRequest／PostToolUse／Notification／Stop／SessionEnd` hook，指令是 `curl -s -o /dev/null --connect-timeout 0.5 -m 1 ... -H @~/.local/share/dev-console/hook-header ... http://127.0.0.1:7691/api/hook?profile=<p>&src=dev-console-hook`（預設設定不帶 `profile=`）` || true`：服務掛掉時 curl 立刻 connection refused（實測約 12ms），不影響 claude。既有 hook（例如通知類 hook）保留。hook 以 `$TMUX_PANE` 對回 tmux session；不在 tmux 的 claude（沒有 `TMUX_PANE`）改以 payload 的 `session_id` 為 key，對回下方的唯讀卡片。兩者都一樣要帶 hook-token，沒帶一律 403。

| 燈 | 來源 |
|---|---|
| 等你允許 | **PermissionRequest**（實測比提示畫面早約 0.04 秒，端到端亮燈 < 0.1 秒）；Notification `permission_prompt` 為備援（實測晚約 6 秒） |
| 問你問題 | PreToolUse `AskUserQuestion`、其他 Notification |
| 工作中（含當前 tool） | UserPromptSubmit／PreToolUse／PostToolUse |
| 做完 | Stop（卡片的最後回覆直接取 Stop payload 的 `last_assistant_message`，並強制重讀 transcript、1 秒與 3 秒後各補算一次） |
| 等你輸入 | Notification `idle_prompt`；或畫面檢查判定提示已取消／工作已中斷（見下） |

AskUserQuestion 與 ExitPlanMode 也會觸發 PermissionRequest（2.1.309 實測）：AskUserQuestion 亮「問你問題」，之後約 6 秒到的 Notification(`permission_prompt`) 不會把它蓋成「等你允許」。

**畫面檢查（hook 收不到的取消與中斷）**：claude 2.1.309 在使用者按 Esc 取消權限提示、取消 AskUserQuestion、或在工作中中斷時，不送任何 hook（沒有 PostToolUse／Stop），燈會一直停在原狀。總覽服務每次輪詢（2 秒）對下列 pane 並行執行 `tmux capture-pane -p -t <pane id>`（一輪總上限 3 秒；同一 pane 至少隔 1 秒才再抓）。

判讀以**輸入框**（兩條整行 `─` 框線、中間第一行 `❯`，下框線之下最多 6 個非空行）為錨點；行數一律只算非空行，高 pane 底部或中間的空白不影響。底部 3 行有 `Showing detailed transcript`（ctrl+o 全畫面 transcript）時一律判斷不出來，對話內容裡的對話框字樣不會讓燈誤亮：

- 權限／問答／計畫確認對話框會取代輸入框；看不到輸入框時，才比對最底 12 個非空行（不含任務清單行，轉小寫、去掉行首 `│ ╭ ╰ ❯`）有沒有 `esc to cancel`／`tab to amend`／`do you want to`／`would you like to`／`enter to select`／`1. yes`。輸入框上方對話裡的問句不算。
- 轉圈行從輸入框上框線往上找（一路找到畫面頂端，50 行以上的新 session 中間可能隔幾十行空白），略過空行、靠右的 effort 列、`tmux …` 提示、`⎿ Tip:`、任務清單（`⎿ ◻ …`、`5 tasks (…)`）；轉圈行要符合「圖示 字… (數字s」，或狀態列有 `esc to interrupt`，才算在跑。
- 閒置的**正面證據**：看得到輸入框、不在跑，而且輸入框上方是 `Interrupted`／`What should Claude do`（窄畫面殘影 `Interrupte·` 也認得）、上一輪的結束行（`✻ … for 2s · done`）、或啟動畫面（送出後很快按 Esc 時 claude 會把 prompt 撤回輸入框，畫面只剩啟動畫面），或狀態列有 `? for shortcuts`。

| 目前燈號 | 何時開始看 | 畫面 | 改為 | 確認次數 |
|---|---|---|---|---|
| 等你允許／問你問題 | 亮燈 1.5 秒後（PermissionRequest 比畫面早約 40ms） | 輸入框回來、沒有對話框、在跑（轉圈行／`esc to interrupt`） | 工作中（按了允許） | 1 次 |
| 等你允許／問你問題 | 同上 | 輸入框回來、沒有對話框、不在跑 | 有閒置正面證據→等你輸入；否則→工作中（交給下一列再判斷） | 連續 2 次 |
| 工作中 | 最後一個 hook 1.5 秒後 | 輸入框正上方是這一輪的結束行、輸入框空、這一輪沒有 Interrupted／User rejected／User declined | 做完 | 連續 2 次 |
| 工作中 | 同上 | 其他閒置正面證據 | 等你輸入 | 連續 2 次 |
| 工作中 | 同上 | 看不到輸入框且有對話框字樣（`Enter to select`→問你問題） | 等你允許／問你問題（補漏收的 PermissionRequest，或修正上面單次升級的誤判） | 連續 2 次 |

- 「連續 2 次」間隔至少 1 秒；第一次看到變化後約 1.15 秒補看一次，不等下一個 2 秒輪詢。實測取消／中斷後 2～4 秒熄燈，送出後幾秒就中斷也一樣（工作中只等最後一個 hook 1.5 秒；舊版等 10 秒，QA 實測要約 13 秒才熄）。按允許後約 1～2 秒變工作中。
- **判斷不出來就維持原燈號**：窄畫面的狀態列不可靠（40 欄實測：auto 模式工作中整段不顯示 `esc to interrupt`，accept edits 被截成 `(shift+tab to  ·`）；工作中打 `/`、`@` 的補全清單會蓋住轉圈行；ctrl+o 全畫面 transcript 沒有輸入框。這些情況都不會熄燈。抓畫面失敗、逾時或畫面為空也一律不動。
- 熄燈要連續兩次確認，是因為手機 ttyd 連進來會改視窗大小，Ink 重畫途中可能拍到半張畫面；熄錯了不會再有 hook 點回來。按允許後的升級只看一次，因為方向錯了只是暫時顯示工作中，會被「工作中看到對話框」那條改回來。
- 改燈以 compare-and-set 寫入，抓畫面期間到達的新 hook 優先；`hooks-state.json` 的 `event` 記為 `screen-check`，`updatedAt` 不變（仍是最後一個真正事件的時間）。
- 一張卡片有多個 pane 時，「等你允許」＞「問你問題」＞其他；同級才比最後事件時間。
- 畫面檢查判定提示已消失後，晚到的 Notification(`permission_prompt`) 不再點亮（假設每個新提示都先有 PermissionRequest；若某次 PermissionRequest 沒送到，該次提示會到下一個 hook 才亮）。
- 畫面判讀的實測樣本在 `internal/hookstore/testdata/`（Bash／Write／Edit／WebFetch／MCP／ExitPlanMode／AskUserQuestion／補充輸入／40 欄各模式／任務清單／補全清單／ctrl+o／各種中斷），claude 改版後先重截樣本再跑 `go test ./internal/hookstore/`。

### transcript 取數口徑（卡片的最後回覆／tool／token）

只看 `type=assistant` 且非 sidechain 的行；Claude 把同一則回覆的每個 content block 各寫一行並重複同一份 usage，所以 token 以 `message.id` 去重後加總。可用以下 jq 複驗：

```bash
jq -s '[.[] | select(.type=="assistant" and (.isSidechain|not))]
  | (group_by(.message.id) | map(last.message.usage)) as $u
  | {input: ($u|map(.input_tokens)|add), output: ($u|map(.output_tokens)|add),
     cacheCreation: ($u|map(.cache_creation_input_tokens//0)|add), cacheRead: ($u|map(.cache_read_input_tokens//0)|add),
     lastText: ([.[].message.content[]? | select(.type=="text") | .text] | last),
     lastTool: ([.[].message.content[]? | select(.type=="tool_use") | .name] | last)}' <transcript.jsonl>
```

「上下文」＝最後一則回覆的 input + cache_creation + cache_read。

### ttyd 健康與自動恢復（ADR 0006）

- **斷線不再漏 fd**：`ttyd-attach.sh` 不再 `exec tmux`，改讓 bash 留著 tty、tmux 在背景跑，tmux 結束後丟掉未讀輸出再退出（原因見下方「坑」）。斷線時先清 tty 輸出佇列再把 HUP 轉給 tmux，3 秒內沒結束就 KILL，保證 ttyd 一定等得到子程序結束。
- **fd 上限**：install.sh 給 ttyd 的 plist 寫 `SoftResourceLimits`／`HardResourceLimits` `NumberOfFiles`（預設 8192；launchd 預設只有 256）。
- **自動恢復**：web 服務每個偵測週期查一次 ttyd 的 fd 數（`launchctl print` 取 pid、`lsof` 數 fd）。達門檻時：
  - 有任何終端機連線 → 不動，web.log 記 `[ttyd] fd N ≥ 門檻 M，延後重啟（有連線 K 條）`，下個週期再看。
  - 沒有連線 → web.log 記 `[ttyd] 觸發自動重啟：fd N ≥ 門檻 M（pid P，上限 L）`，`launchctl kickstart -k` 重啟 ttyd；重啟期間新的 `/term/ws` 回 503，等新 socket 有回應才放行。tmux session 不受影響。
  - 「有連線」以 dev-console 反代追蹤的活 WebSocket 數為準：ttyd 只聽 0700 目錄裡的 unix socket，唯一連入者就是反代，所以這個數就是 ttyd 的全部連線；不看 ttyd 子程序，因為卡死的殘留子程序會被誤判成有人在用、永遠不重啟。
- **調整門檻與週期**（寫進 web 服務的 plist，重跑 install.sh 生效；不帶變數重跑就回到預設。install.sh 會先驗格式：週期必須是 Go duration，門檻必須 >0 且小於上限，不合法就中止；手動改 plist 寫錯時服務改用預設值並在 web.log 記一行）：

  | 變數 | 預設 | 說明 |
  |---|---|---|
  | `DC_TTYD_FD_RESTART` | `2048` | ttyd fd 數達此值就（在沒人連線時）重啟；必須小於 `DC_TTYD_NOFILE` |
  | `DC_TTYD_WATCH_INTERVAL` | `60s` | 偵測週期（Go duration，要帶單位如 `10s`、`2m`；最短 5s；`0` 關閉自動重啟，狀態查詢照常） |
  | `DC_TTYD_NOFILE` | `8192` | ttyd 的 fd 上限（≥1024）；剩不到 5% 時健康狀態判為異常 |

  例：QA 測自動恢復 `DC_TTYD_FD_RESTART=10 DC_TTYD_WATCH_INTERVAL=10s ./install.sh`，測完 `./install.sh` 還原。
- **終端機頁提示與退避**：ttyd 以 `-t disableReconnect=true` 關掉它前端「斷線立刻重連、沒有退避」的行為，改由注入的 `termguard.js`（同步 script，早於 ttyd 自己的 script）處理：終端機 WebSocket 不正常關閉時查 `GET /api/term/health`，服務異常就在頁面頂端顯示紅色「終端機服務異常：<原因>」並依 1、2、4、8、15、30 秒（之後固定 30 秒）退避再查，只查健康 API、不打 `/term/ws`；恢復後自動重新載入頁面接回。每次重新載入也計一次失敗，連上的連線撐過 15 秒才把退避歸零（記在 sessionStorage，連線一直被立刻關掉也不會變成重載風暴）。tmux session 正常結束（WebSocket code 1000）維持 ttyd 原本的「Press ⏎ to Reconnect」，不自動重連。ttyd 連不上時直接開 `/term/?arg=` 會拿到 503 替代頁（同樣的提示與自動恢復）。
- **健康狀態**：`scripts/check.sh ttyd`（只看 ttyd）或 `scripts/check.sh`（最後一列）印 `ttyd：OK（pid P，fd N／上限 L，自動重啟門檻 M）`，異常時印原因且結束碼 1；總覽頁標題列有「終端機 OK（fd N／L）」標籤，每 30 秒更新，異常時標紅。異常（`ok=false`）只有：ttyd 沒在執行、socket 沒回應、無法讀取 fd 數、fd 逼近上限（剩不到 5%）、重啟中。fd 超過自動重啟門檻但終端機仍可用時是**警告**：`ok=true`＋`warning`，check.sh 結束碼 0 並附「注意：…」，總覽頁標橘，終端機頁不顯示異常提示。API：`GET /api/term/health`（需身分）回 `{ok, reason, warning, pid, fds, limit, threshold, activeConns, restarting, checkedAt}`。
- **log 節流**：反代連不上 ttyd 的 `[term] ttyd 連線失敗`、健康檢查 `[ttyd] 讀不到 fd 數` 同類 30 秒只記一次，下一次記錄時附「前 30s 內另有 N 次」。

### 任務與看門狗

這一區是**選用**的：它顯示作者自己另一套工作流程寫在 `~/.claude/state/` 的任務狀態檔。沒有這些檔案的機器上，這一區就是空的，不影響其他功能，也不需要建立任何檔案。

- 任務：`~/.claude/state/*.json`（status、task、next_action、acceptance），`awaiting_user` 排最前且標紅。`running`／`awaiting_user`／`awaiting_next_batch` 與 14 天內有更新者直接展開，其餘收進「較舊／已完成（N）」摺疊區。解析失敗的 state 檔在錯誤區列出檔名（不修改）。
- 驗收進度（state 的 `acceptance` 欄位指向的清單）：條目＝不重複的 `### <大寫字母><數字>`；已驗＝`evidence/` 第一層一般檔案（跟隨 symlink）檔名含 `<條目>-` 的條目數（`qa-A3-x.png`、`pm-A12-y.txt` 都算）。條目前一個字元不能是英數，`DATA10-` 不算 A10（單純的 glob `*A10-*` 會誤配）。
- 看門狗：`~/.claude/state/watchdog.log` 最後一行與 `.last-sweep` 的 mtime。
- 全部唯讀，不寫 state。

## 逃生開關

| 情境 | 做法 |
|---|---|
| `aiuse claude`／`aiuse codex` 不要自動進 tmux | `AIUSE_NO_TMUX=1 aiuse claude <profile>`、`AIUSE_NO_TMUX=1 aiuse codex <profile>` |
| aiuse 自動判斷不進 tmux（claude） | 已在 tmux 內、stdin/stdout 不是 TTY、`-p/--print`、`--version`、`--help`、`--bg`、管理子命令（mcp、auth、doctor…） |
| aiuse 自動判斷不進 tmux（codex 一般 profile） | 已在 tmux 內（dev-console 臨時 session 除外）、stdin/stdout 不是 TTY、`exec`／`login` 等已知子命令（清單取自 `codex --help`）、`--version`、`--help`、明確 `--remote`；互動 TUI、`resume`、`fork`、以 prompt 開頭（`aiuse codex team "修這個 bug"`）都會進 tmux |
| profile 名稱 | 英數、底線、連字號，首字不可是 `-`（aiuse 與 dev-console 同一套）；大小寫以磁碟上的目錄名為準：aiuse 遇到大小寫不同的同名目錄（例如 `TEAM` 在已有 `team` 目錄時）改用目錄原樣名稱；dev-console 選單與 API 只收目錄原樣名稱 |
| `AIUSE_TMUX_NO_ATTACH` | 不是使用者開關：只在 dev-console 的 `dc-launch-<token>` 臨時 session 內、值等於 token 才生效；其他情況帶了它，互動 codex 會直接報錯退出（unset 後重試） |
| 暫停網頁存取 | `tailscale serve --https=443 --set-path=/ off`（只移除本服務的根路徑；重跑 install.sh 會恢復） |
| 停所有服務 | `launchctl bootout gui/$(id -u)/<DC_LABEL_PREFIX>.dev-console.web`（ttyd、rc-<名稱> 同理） |
| rc 不自動回答家目錄信任提示 | plist 加 `DC_RC_AUTO_TRUST=0` |

## 換機步驟

1. **clone**：`git clone https://github.com/chiyum/remote-console.git`。要用 aiuse 模式的話，先自行裝好 aiuse 與 `~/.ai-profiles`。
2. **install**：`cd remote-console && DC_LABEL_PREFIX=... DC_ALLOWED_LOGINS=... DC_TAILNET=... ./install.sh`；若印出 serve 啟用網址，按啟用後再跑一次。
3. **Tailscale ACL**：新機器若掛 tag，`TS_API_TOKEN=... scripts/tailscale-acl.sh --src <你的帳號> --ip <新機 tailnet IP>`；舊機規則到 admin console 刪掉。
4. **能源不休眠**：`sudo pmset -c sleep 0 disksleep 0`（或 `caffeinate -s` 常駐）；系統設定 → 共享 → 遠端登入 開啟（SSH attach 用）。
5. **驗證**：
   ```bash
   launchctl list | grep dev-console                     # web、ttyd（與選用的 rc-<名稱>）都有 PID
   lsof -nP -iTCP -sTCP:LISTEN | grep dev-cons           # 只有 127.0.0.1:7690/7691
   tailscale serve status && tailscale funnel status     # serve 有 443、funnel 無條目
   curl -sI https://<新機>.<tailnet>.ts.net/              # 200（不加 -k）
   tmux ls | grep rc-                                    # 有裝 remote-control 常駐時
   ```
6. 舊機解除安裝（見下）。

## 解除安裝

> **警告**：uninstall.sh 會刪除整個資料目錄 `~/.local/share/dev-console`，**包含已配對裝置的紀錄**（之後每台瀏覽器要重新配對）、session 暱稱與封存紀錄、燈號狀態、hook-token 與 `install.env`。只是要升級的話請直接重跑 `./install.sh`，不要先解除。

```bash
./uninstall.sh                                 # label 前綴自動從 install.env 讀回
TARGET_HOME=<目錄> ./uninstall.sh               # 只產檔過或目標不是本機家目錄：自動只刪檔，不呼叫 launchctl／tailscale
```

停 launchd 服務、刪 `<前綴>.dev-console.*` plist（同前綴的其他 plist 不動）、移除 settings.json 裡的 dev-console hook（其他 hook 不動）、移除 tailscale serve 443 上指向本服務的根路徑 handler（`--set-path=/ off`；443 上其他路徑不動）、刪 `~/.local/share/dev-console` 與 `~/Library/Logs/dev-console`（`DC_KEEP_LOGS=1` 保留 log）、關掉本工具建立的 `rc-*` tmux session。只產檔或正式啟用由 `install.env` 的 `DC_INSTALL_MODE` 自動判斷（`NO_LOAD=0／1` 可明確指定）；已解除過再跑一次會印「看起來已解除」並以 0 結束。不動：ttyd／tmux（`brew uninstall`）、aiuse（另一個工具，不屬本 repo）、Tailscale ACL（admin console 手動刪）、`~/.tmux.conf` 的 window-size 行、其他 tmux session。

## 開發

```bash
scripts/check.sh        # gofmt、go vet、go test、shell 語法與「$變數緊接中文」檢查，最後一列是本機 ttyd 狀態
scripts/check.sh ttyd   # 只看本機 ttyd 狀態（異常時結束碼 1）
```

```bash
scripts/test-install-isolation.sh   # 以假的 tmux／launchctl／tailscale／brew／ttyd 驗證只產檔模式完全不碰真機（呼叫紀錄必須為空）
scripts/check-shell-vars.sh         # 靜態檢查 .sh 裡「$變數 後面緊接非 ASCII 字元」（check.sh 也會跑）；有命中就列出並以 1 結束
```

### 測試時隔離 tmux（必讀）

你自己的 Claude／Codex session 多半跑在 tmux 裡，而 tmux 用戶端會**優先使用 `$TMUX` 指向的 socket**，`TMUX_TMPDIR` 在 tmux 裡面完全隔不開。在 tmux 裡對「以為是隔離的 server」下 `kill-server`，會關掉你真正的所有 session。規則：

- 測試碰 tmux 一律帶 `-S <專用 socket>`（或 `-L <專用名>`），而且前面加 `env -u TMUX -u TMUX_PANE`；不帶 `-S`／`-L` 的 tmux 只做唯讀的 `list-sessions`。
- 用隔離 server 前先做負向對照：隔離 server 的 `list-sessions` 不能出現你平常的任何 session，出現就停手。
- 受測的 dev-console 以 `env -i` 啟動，`DC_TMUX_PATH` 指向只會帶 `-S` 的包裝腳本（不要依賴 `TMUX_TMPDIR`）；關 ttyd 自動重啟（`DC_TTYD_WATCH_INTERVAL=0`）並給一個不存在的 `DC_TTYD_LABEL`，避免它對真機的 launchd 下 kickstart。
- 停程序只用自己記下的 PID，不用 `pkill -f`。

```bash
SOCK=/tmp/rc-test.sock                     # 路徑要短：macOS unix socket 上限約 104 bytes
cat > /tmp/rc-test-tmux <<EOF
#!/bin/sh
exec env -u TMUX -u TMUX_PANE /opt/homebrew/bin/tmux -S $SOCK "\$@"
EOF
chmod 755 /tmp/rc-test-tmux
/tmp/rc-test-tmux list-sessions           # 負向對照：應回 no server running／空清單，不能看到你的 session

env -i HOME=/tmp/rc-fake-home PATH=/tmp/rc-fake-bin:/usr/bin:/bin DC_SESSION_PATH=/tmp/rc-fake-bin:/usr/bin:/bin \
  DC_TMUX_PATH=/tmp/rc-test-tmux DC_LISTEN_ADDR=127.0.0.1:7790 DC_HOOK_ADDR=127.0.0.1:7791 \
  DC_TTYD_WATCH_INTERVAL=0 DC_TTYD_LABEL=local.rc-test.none DC_ALLOWED_LOGINS=you@example.com \
  ./dev-console & WEB_PID=$!
# …測試…
kill "$WEB_PID"
/tmp/rc-test-tmux kill-server             # 只關隔離的 server
```

### 坑

- **bash 變數後面直接接中文**：UTF-8 locale 下 macOS 內建的 bash 3.2 會把 `$name：` 的全形字併進變數名，`set -u` 時噴「未綁定的變數」。一律寫 `${name}`；`scripts/check-shell-vars.sh`（`scripts/check.sh` 會呼叫）會擋，提交前務必跑。
- **bash 3.2 的 EXIT trap 會吞掉「未綁定的變數」的失敗**：`set -u` 中止時若有 EXIT trap，腳本以 trap 的狀態碼（通常 0）結束，trap 裡的 `$?` 也是 0。install.sh 等腳本因此用「完成旗標」：沒走到正常結束點就在 trap 裡以非 0 結束。`scripts/test-install-isolation.sh` 的情境 6b 會驗證。
- **不可就地覆寫正在執行的 bash 腳本**：`aiuse claude` 以子程序跑 claude、bash 還開著腳本（fd 255），用 `cat >`／`cp` 覆寫同一 inode，claude 結束後 bash 會從舊位移讀到新內容亂跑。安裝一律 tmp + `mv`。
- **remote-control 的 `--spawn worktree` 在非 git 目錄（`~`）直接失敗**，所以 rc-<名稱> 用 `same-dir`。家目錄的信任提示每次啟動都會問（不會被記住），由 rc-keeper 偵測後回 `y`。
- **tmux 的 `-t =名稱` 只適用 session 目標**：`set-option`／`display-message` 這類以 pane 為目標的指令要寫 `=名稱:`，否則找不到 session；加了 `-q` 時是默默失敗（rc-keeper 初版的 remain-on-exit 因此沒生效）。
- **tmux argv 結尾是 `;` 的參數會被當成指令分隔**：`tmux set-option -t $1 @x 'a;'` 存進去的是 `a`（3.7c 實測），所以暱稱結尾不可是分號。
- **iTerm2 標題 unset 不會復原**：tmux 只在 `set-titles on` 時送標題，直接 unset 後 iTerm2 停在最後一次的標題；要先送空標題再 unset。
- **tmux `send-keys -t` 是 pane 目標**：不能寫 `=名稱`；按鍵 API 先從 `list-sessions` 以精確名稱取 `$N` 再送。
- **全螢幕程式在 tmux 裡沒有 scrollback**：Claude／Codex 的 pane `alternate_on=1`、`history_size` 近 0，copy-mode 捲不到它們的對話；要捲得送程式自己認得的鍵或滾輪序列（ADR 0009）。tmux copy-mode 的 `page-up` 一次捲「pane 高度 − 2」列。
- **`cat -v` 不會把 Tab 顯示成 `^I`**：驗按鍵順序用 `cat -vt`，而且只按不送 Enter 時畫面上是 tty 回顯（Tab 顯示成空白），送 Enter 後 cat 印出的那一行才是 `^I`。
- **ttyd 斷線時 `exec tmux` 會死結漏 fd（macOS）**：網頁斷線時 ttyd 1.7.7 停讀 pty、對子程序群組送 SIGHUP，等子程序結束才 close master。tmux client 收 HUP 會寫還原終端的控制碼；macOS 最後一次 close tty 會等輸出排空且沒有逾時，沒人讀 master 就永遠排不空：tmux 卡在 `E` 狀態、ttyd 每次漏 3 個 fd（master＋2 dup），撞到 launchd 預設上限 256 後所有人都開不了終端機（`pty_spawn: 24 (Too many open files)`）。瀏覽器的 xterm 寫不完時會送 PAUSE 讓 ttyd 停讀 pty，所以手機最容易觸發；乾淨斷線不一定重現。修法是讓 bash 留著同一個 tty、tmux 背景跑，結束後 `tcflush(TCOFLUSH)` 再退出。上游 1.7.7 未修。
- **launchd 的 PATH 很短**：tmux server 若由 launchd 這邊先起，之後所有 session 都繼承短 PATH；所以 plist 與新開 session 都明確帶完整 PATH。

## 授權

MIT，見 [LICENSE](LICENSE)。
