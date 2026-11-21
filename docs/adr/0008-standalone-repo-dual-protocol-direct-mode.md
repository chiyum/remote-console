# ADR 0008：抽成獨立 repo；Codex 認領同時相容有／無 token 兩代 aiuse；沒有 aiuse 的直接模式

- 狀態：已採用（2026-11-21），修訂 ADR 0007 的認領協定部分
- 決策者：擁有者（獨立模組、公開、無 aiuse 的機器要能從網頁開 Claude 與 Codex、擁有者自己的機器日常操作不受影響）、architect（相容做法、並發保護、直接模式設計）

## 背景

遠端工作台原本有兩份程式：擁有者自用的版本，以及放在私人工作流 repo 裡、個人值改成安裝參數的版本（ADR 0007）。要讓朋友只裝這一個模組、並且公開原始碼，需要抽成獨立 repo（全新 git 歷史）。抽出時遇到兩個相容問題：

1. **Codex 認領協定分成兩代**。工作流版只認「`codex-direct-v1`＋`@aiuse_launch_token` 等於本次 token」。擁有者現用的 aiuse 不寫 token，而且部分 profile 走 standalone 嚴格路徑、標 `codex-v1`（aiuse 會把 profile 轉小寫）。直接換裝工作流版，擁有者從網頁開 Codex 會全部失敗。token 的用意是防止「同一 profile 同時開兩個、互相認領錯」，相容舊協定時不能讓這個防護失效。
2. **不是每台機器都有 aiuse**。朋友的 Mac 可能只有 `claude`／`codex`，原本網頁新開 session 完全依賴 aiuse。

## 決策

### 獨立 repo

- 模組名 `github.com/chiyum/remote-console`，全新歷史；服務、binary、資料目錄與 launchd label 仍叫 `dev-console`，既有安裝可原地覆蓋（label、資料目錄、serve 目標都不變）。
- 安裝者的個人值（label 前綴、允許登入帳號、tailnet、常駐名稱）存在本機 0600 的 `install.env`，重跑安裝時沒給的參數從這裡讀回；repo 內不放任何個人值。
- 程式與文件不寫死任何 profile 名稱。

### Codex 認領：兩代協定都認，靠標記判斷

- 接受 `@aiuse_managed` 為 `codex-direct-v1`（profile 完全相同）或 `codex-v1`（profile 不分大小寫）的新 session；哪個 profile 走哪種由 aiuse 決定，dev-console 不維護名單（不加回 `DC_CODEX_STANDALONE_PROFILES`）。
- 帶 `@aiuse_launch_token` 的：必須等於本次 token，相符立刻認領；不符一律略過。
- 不帶 token 的（舊版 aiuse）：
  1. **同 profile 串行化**：同一個 profile（不分大小寫）的 Codex 新開以容量 1 的 channel 排隊，鎖涵蓋「拍下建立前 session 集合 → 建 helper → 認領 → 等 helper 結束並刪除」整段。後一次開始時，前一次的 session 已在它的「建立前」集合裡，新出現的只會是自己 helper 建的那個。
  2. **只在 helper 的 aiuse 以 exit 0 結束後才接受**：新版 aiuse 一定在結束前寫好 token 並標 ready，所以它成功時看到的必定是帶 token 的那個；它失敗（非 0）時不會退而認領別人在本機終端機手動開的無 token session。因此新版 aiuse 的「只認自己 token」保證完全不被相容路徑削弱。
  3. **候選必須唯一**：同時出現兩個以上無 token 的新 session（例如本機另一個終端機剛好也開了同 profile）時回錯誤，不猜。
- 等效性：token 防的是「網頁同時兩次新開互相認領錯」。串行化讓網頁端的兩次新開在時間上不重疊；建立前後集合差異排除所有既有 session；exit 0＋唯一候選排除 aiuse 失敗與外部同時新開的情況。剩下無法區分的只有「網頁新開進行中、本機另一處在同一秒用舊版 aiuse 開同 profile」，此時會因候選不唯一而回錯誤，不會認錯。

### 直接模式（沒有 aiuse）

- 判斷：`DC_AIUSE_PATH`（預設 `<HOME>/.local/bin/aiuse`）不是存在、可執行的一般檔案。服務每次新開 session 都重新判斷，install.sh 與 rc-keeper 用同一條規則。
- `/api/profiles` 回 `mode`（`aiuse`／`direct`）；直接模式前端隱藏 profile 選單，可選 Claude 或 Codex 與目錄。
- 執行 `claude`／`codex`：先解析成絕對路徑（`DC_CLAUDE_BIN`／`DC_CODEX_BIN` 覆寫，否則在 session PATH 的絕對目錄找），找不到在建立任何 tmux session 之前就回錯；以 exec 陣列交給 tmux，不經 shell。
- session 名 `dc-<tool>-<UTC>-<hex>`、標 `@dc_created=1`，不寫任何 `@aiuse_*` 標記（不冒充 aiuse 管理的 session）；總覽頁依名稱前綴判斷工具。
- 輸入驗證不放寬：任何 profile 值都拒絕（不會進 argv）；目錄規則與 aiuse 模式相同；寫入 API 的 cookie／Origin／`X-Dev-Console` 檢查不變。
- 手機遙控常駐（`DC_RC_PROFILE`）：直接模式改執行 `claude remote-control`（預設帳號），`DC_RC_PROFILE` 只用來命名。正式安裝時找不到 `claude` 直接中止，不裝出一直失敗的常駐服務；只產檔模式只警告。rc-keeper 本身找不到 claude 時只記 log、冷卻後重試，不退出，不會讓 launchd 進入快速重啟迴圈。

## 未採方案

- **以設定檔列出 standalone profile**（舊 `DC_CODEX_STANDALONE_PROFILES`）：等於把 aiuse 的決定抄一份，兩邊不同步時就認領不到；而且會把個人 profile 名稱帶進設定與文件。
- **無 token 路徑只靠「建立前後集合差異」、不串行化**：同 profile 兩次網頁新開重疊時，兩邊都看到兩個新 session，會互相認錯（單元測試以拿掉鎖的突變驗證會失敗）。
- **改擁有者的 aiuse 讓它寫 token**：超出範圍（擁有者明確要求 aiuse 不動）。
- **直接模式沿用 profile 選單、以 `~/.claude` 當唯一 profile**：會讓 transcript／唯讀卡片看起來支援、實際卻讀不到，誤導使用者；改為明列已知限制。
- **直接模式的常駐遙控一律報錯中止**：朋友照文件選「要常駐」時無法完成安裝；`claude remote-control` 本身就能在預設帳號運作。

## 後果

- 既有安裝（沒有 `install.env` 的舊版）可原地覆蓋：label、資料目錄、serve 目標不變，配對與暱稱保留；第一次覆蓋後會產生含個人值的 `install.env`。
- 同 profile 的 Codex 網頁新開改為排隊，第二個要等第一個完成（通常數秒）。
- 直接模式下 iTerm 等非 tmux 直開的 session 不顯示、卡片不顯示對話內容（README「兩種模式」）。
- procscan 與 aiuse 的 CLI 清單同步測試改比對 repo 內節錄（`internal/procscan/testdata/aiuse-cli-surface.sh`），`DC_AIUSE_SOURCE` 可改比對實際安裝的 aiuse。
