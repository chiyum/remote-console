# ADR 0007：給別人安裝時改為選用模組，個人值全部改成必填參數，並提供只產檔模式

- 狀態：已採用（2026-10-28）
- 決策者：擁有者（遠端工作台選用、預設不啟用、不做一鍵安裝）、architect（參數命名、必填與預設、解除範圍）
- 背景說明：當時本模組放在擁有者的另一個（私人）工作流 repo 裡當選用子目錄；之後抽成獨立 repo，見 ADR 0008。本 ADR 的參數化決定在獨立 repo 照樣適用。

## 背景

原本的安裝腳本寫死擁有者自己的 launchd label 前綴、允許登入帳號、remote-control 保活的 profile 與兩個走 standalone 路徑的 Codex profile。要給別人安裝後，這些預設值不是錯就是危險（預設放行某個特定帳號）。另外驗收必須在假家目錄跑完整的啟用與解除，不能碰真機的 launchd 與 Tailscale。

## 決策

- **label 前綴、允許登入帳號、tailnet 名稱為必填**，沒給就中止。允許登入不給預設值：寧可漏填而連不進來，也不要預設放行某人；Go 端 `DC_ALLOWED_LOGINS` 沒設時同樣是空名單（任何帶 Tailscale 身分的請求都拒絕）。
- **實際 label 為 `<前綴>.dev-console.*`**：同一前綴可能被安裝者其他 plist 共用，解除時只刪這一組。
- **安裝紀錄 `install.env`**：記下前綴、目標家目錄與 port，解除不必再問參數，也不會用錯前綴。只以 `sed` 取鍵值，不 `source`。
- **tailnet 比對**：正常模式拿 `tailscale status` 的主機名比對使用者填的 tailnet，不符就中止，避免把 serve 開在別的 tailnet。
- **remote-control 保活改選用**（`DC_RC_PROFILE` 預設空）；rc-keeper 沒有預設 profile。
- **standalone Codex profile 名單參數化**（`DC_CODEX_STANDALONE_PROFILES`，預設空＝全部走 `codex-direct-v1`）：哪些 profile 走 standalone 由使用者的 aiuse 決定，dev-console 只需與之一致。
- **`TARGET_HOME` 與 `NO_LOAD=1`**：所有路徑以目標家目錄計算；只產檔模式不執行 launchctl、tailscale、tmux server、brew 安裝，讓驗收與「先看產物再決定」都能在假家目錄完成。解除也有對應的只刪檔模式。
- **hooks 也合併進 `~/.claude/settings.json`**（不帶 profile 參數），供不使用 profile 的安裝；settings.json 是 symlink 時照舊略過，不改共用檔。
- **不再從其他目錄同步 aiuse**；aiuse 是另一個工具，由使用者自行安裝（沒有它的處理見 ADR 0008 的直接模式）。

## 後果

- 安裝指令變長（三個必填參數），README「安裝」列出三個設定題讓安裝者先想好再執行。
- 精簡版 aiuse 沒有 tmux 相關功能時，網頁開 Codex session 會逾時失敗；其餘功能不受影響（README「與 aiuse 的相依」）。
- 舊安裝的 plist label 與新前綴不同，換成公版前要先用舊版解除腳本解除。

## 修訂（2026-10-28，reviewer 退修）

- 撤回 `DC_CODEX_STANDALONE_PROFILES`：當時的 aiuse 只有 `codex-direct-v1` 一條路徑，名單填了只會讓網頁新開 Codex 永遠認領不到；install.sh 遇到非空值直接中止並說明。（ADR 0008 改為依 session 標記同時支援兩種協定，名單仍不需要，install.sh 改為提醒後忽略。）
- 認領條件加上 `@aiuse_launch_token`（＝helper 的 token），網頁同時開兩個 Codex session 時各自認領自己的。（ADR 0008 補上不寫 token 的舊版 aiuse 的等效並發保護。）
- 解除只移除 tailscale serve 443 根路徑上指向本服務的 handler（`--set-path=/ off`），安裝遇到根路徑已被別的服務佔用時停下，不覆寫。
