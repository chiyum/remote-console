# ADR 0004：session 暱稱與封存存在 dev-console 自己的檔案，Claude 以 session id 為 key；封存＝結束 tmux session

- 狀態：已採用（2026-10-26）
- 決策者：擁有者（封存＝結束 session）、主 Claude／architect（儲存與 key）

## 背景

擁有者要能替 session 取暱稱、把沒用的 session 封存掉不佔畫面，並讓 iTerm2 tab 標題自動帶入暱稱。擁有者裁決封存要真的結束 tmux session（「反正可以用 /resume 找回來」），已封存區要能看到暱稱與接手指令、可取消封存。

## 決策

- 暱稱與封存紀錄存 `~/.local/share/dev-console/session-meta.json`（0600、tmp＋rename 原子寫入）。不存在 tmux option：封存後 session 已不存在，已封存區仍要顯示；`--resume` 重開是新的 tmux session。
- 暱稱 key：Claude 已知 session id 時只讀寫 `claude:<session id>`（tmux 卡片取 hook 的 session id，還沒有 hook 時讀 `<profile>/sessions/<pid>.json`；唯讀卡片取程序表的），所以 `--resume` 後暱稱自動帶回。session id 未知時用 `tmux:<名稱>`，之後 `claude:<sid>` 完全沒紀錄時搬過去一次並刪掉；沒有 session id 的唯讀卡片用 `proc:<pid>:<啟動秒>`。清空寫空字串墓碑。
  - 第 1 版是「同一張卡片的多個 key 以最後更新者為準互相同步」，reviewer 指出同一個 tmux 裡 `/resume` 切到另一段已有暱稱的對話會被改寫，改成上面的單一權威 key。
- 隱藏：只有唯讀卡片的紀錄（key＝`proc:<pid>:<啟動秒>`）讓仍存活的同一程序維持隱藏。tmux 卡片紀錄的 key 是 `tmux:<名稱>@<封存時間 ns>`，只當已封存區歷史：第 1 版以 `tmux:<名稱>` 隱藏，`tmux new` 出同名 session（例如 `0`）會被永久藏住，且同名再封存會覆寫舊紀錄。
- 自動移除（擁有者裁決，A10）：同一個 Claude session id 以新的活著的卡片重新出現（tmux 卡片，或 pid 不同於封存時的唯讀卡片）時移除該紀錄；封存時的程序本身還活著、或同名 tmux session 尚未被結束（寫完紀錄到 kill 之間），都不算。
- 封存 tmux 卡片：先寫紀錄、再 `kill-session -t $N`（`$N` 必須出現在當下的 `list-sessions` 結果中），kill 失敗只撤回這一筆。唯讀卡片只寫紀錄，不送任何訊號給程序。
- 寫檔：所有修改在表的複本上做，寫檔成功才換上，失敗時記憶體與磁碟一致。
- iTerm2 標題：對有暱稱的 session 設 session 層 `set-titles on`、`set-titles-string '#{@dc_nickname}'`、`@dc_nickname`；標題字串只引用 option，暱稱裡的 `#(...)` 不會被展開。清空時先送空標題，下一輪（≥2 秒）再 unset 回全域預設。

## 取捨

- Codex 沒有 session id，用 `aiuse codex <p> resume` 重開後暱稱不會帶回（已列為非目標）。
- `claude:` key 會隨 session 累積，以 2000 筆上限淘汰最舊的；`tmux:`／`proc:` key 在 session 消失後清掉（只在 tmux 與程序表都查詢成功的那一輪）。
- 回滾：revert 即可，`session-meta.json` 留著無害；已設過暱稱的 tmux session 會留著 session 層 `set-titles` 等 option，直到 session 結束或手動 `set-option -u`。
