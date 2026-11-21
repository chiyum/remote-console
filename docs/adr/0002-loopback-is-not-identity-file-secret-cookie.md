# ADR 0002：loopback 不是身分——改用檔案秘密（配對碼換 cookie、hook token）

- 狀態：已採用（2026-10-23，reviewer／security-auditor 退修第 1 輪），取代 ADR 0001 的「身分」段
- 決策者：architect（依退修指示）

## 背景

ADR 0001 以 header 判斷身分：本機直連（無 XFF、Host 為 127.0.0.1）一律放行，經 serve 時信 `Tailscale-User-Login`／`X-Forwarded-For`。實測 Docker 容器經 `host.docker.internal` 連到主機 127.0.0.1:7690、帶 `Host: 127.0.0.1:7690` 就被當成本機（`{"login":"local"}`），也能偽造 serve 的所有 header——因為 serve 和容器在主機上都是 127.0.0.1 的 TCP 連線，伺服器分不出來。

## 考慮過的方案

1. **UI／hook 改聽 0700 目錄下的 unix socket，serve 目標改 `unix:`**：容器碰不到。實測 App 版 Tailscale 1.102.3（系統延伸以 root 在沙箱內）serve 到 unix socket 一律 502，連 /tmp 下 0666 的 socket 也一樣 → 不可行。
2. **辨識對端程序**：serve 的連線屬於 root 的 network extension，使用者層 lsof 看不到；容器連線屬 Docker backend（使用者程序）看得到。可行但是推論式啟發法，換版本就可能失效，不當安全邊界。
3. **檔案秘密（採用）**：瀏覽器以一次性配對碼（0600 檔，10 分鐘、單次、錯 5 次作廢）換 HttpOnly cookie（伺服器只存 SHA-256）；hook 每次帶 0600 hook-token 檔內容。header 只用來拒絕（Host 白名單擋 DNS rebinding、非擁有者 login 直接拒）。

## 決策與理由

採方案 3：唯一不依賴網路來源判斷的做法，容器、其他本機程序、tailnet 上其他人都拿不到 0600 檔。代價是每台瀏覽器（含 Playwright context）第一次要配對；這對單人工具可接受。寫入與 WebSocket 一律要求相符 Origin，本機直連不再豁免。

## 影響與回滾

- 手機第一次使用要在 Mac 執行 `dev-console pair` 取碼。撤銷：`dev-console revoke-all`。
- 回滾＝revert 本 commit 並重跑 install.sh（hooks 指令會換回不帶 token 的版本）；會重新打開容器冒充的洞，不建議。

## 補充（退修第 2 輪，security-auditor N1–N3）

- **cookie 不綁 127.0.0.1**：cookie 不分 port，綁 127.0.0.1 的 cookie 會被瀏覽器送到同機任何 port（例如容器開的 `127.0.0.1:8080`），對方可重放。本機改用專用名稱 `dev-console.localhost`（瀏覽器與 curl 都解析到 loopback）並列入 Host 白名單；127.0.0.1／localhost 不發也不認 cookie。本機 cookie 效期 12 小時，serve（HTTPS、Secure）90 天。
- **配對改 POST＋Origin**：配對碼不留在網址與瀏覽紀錄，也擋跨站把別人的碼塞進你瀏覽器。CLI 印的 `/?pair=<碼>` 由頁面自動 POST 後清掉網址。
- **hook token 走 `curl -H @<0600 檔>`**：原本 `$(cat …)` 展開後 token 會出現在 `ps` 的命令列參數。

