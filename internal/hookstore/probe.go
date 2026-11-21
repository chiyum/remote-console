package hookstore

import "time"

// 畫面檢查（screen-check）：hook 不會回報「取消／中斷」，由 snapshot 定期抓 pane 畫面交給 Observe 判定。
//
// 降級一律要「連續兩次沒看到」：手機以 ttyd 連進來會改 tmux 視窗大小，Ink 重畫途中 capture 可能拍到
// 只畫一半的畫面（實測 40 欄時拍到「Interrupte· What should」）；熄錯燈之後不會再有 hook 把它點回來，
// 所以寧可慢一輪也不能誤熄。

// EventScreenCheck 標示燈號是由畫面檢查（而非 hook）改變的，方便從 hooks-state.json 追查。
const EventScreenCheck = "screen-check"

// MinMissGap 是兩次「沒看到」之間最短的間隔：hook 觸發的重算可能在幾毫秒內連跑兩次，
// 那兩張畫面拍的是同一個重畫瞬間，不算兩次獨立確認。
const MinMissGap = time.Second

// MinProbeGap 是同一個 pane 兩次抓畫面的最短間隔：hook 爆量時每個事件都會觸發重算，不能每次都抓。
const MinProbeGap = time.Second

// Probe 是一個待看畫面的 pane 與當時的狀態（用來做 compare-and-set）。
type Probe struct {
	Pane      string
	State     string
	UpdatedAt time.Time
}

// miss 是第一次看到變化的紀錄；State／目標燈號／UpdatedAt 任一對不上就作廢重算。
type miss struct {
	state     string
	target    string
	updatedAt time.Time
	at        time.Time
}

// Probes 列出需要看畫面的 pane：
//   - 等你允許／問你問題：亮燈至少 promptGrace（PermissionRequest 比對話框早約 40ms 送達，太早看會誤判）
//   - 工作中：最後一個 hook 至少 workingIdle 之前（工作中按 Esc 中斷不送 Stop，燈會一直停在工作中）
//
// 同一個 pane 距上次列入不到 MinProbeGap 就跳過。
func (s *Store) Probes(promptGrace, workingIdle time.Duration) []Probe {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var res []Probe
	for p, st := range s.byPane {
		age := now.Sub(st.UpdatedAt)
		switch {
		case (st.State == StatePermission || st.State == StateQuestion) && age >= promptGrace,
			st.State == StateWorking && age >= workingIdle:
			if last, ok := s.probed[p]; ok && now.Sub(last) < MinProbeGap {
				continue
			}
			s.probed[p] = now
			res = append(res, Probe{Pane: p, State: st.State, UpdatedAt: st.UpdatedAt})
		}
	}
	return res
}

// Outcome 是一次畫面判讀的結果。
type Outcome int

const (
	// OutcomeKeep：燈號不變（提示／工作還在，或判斷不出來）。
	OutcomeKeep Outcome = iota
	// OutcomePending：第一次看到變化，等下一次確認；呼叫端應在 MinMissGap 後盡快再看一次。
	OutcomePending
	// OutcomeChanged：燈號已改。
	OutcomeChanged
)

// Observe 套用一次畫面判讀。
//
// 轉換規則（判斷不出來一律維持原燈號）：
//   - 等你允許／問你問題 → 工作中：輸入框回來、沒有對話框、而且看得到在跑（轉圈行／esc to interrupt）。
//     這是使用者按了允許，只確認一次就升級：方向錯了（例如重畫時拍到半張畫面）只是暫時顯示工作中，
//     下面「工作中 → 提示」那條會在兩次確認後把燈改回來。
//   - 等你允許／問你問題 → 等你輸入或工作中：輸入框回來、沒有對話框，連續兩次。有閒置正面證據才是
//     等你輸入，否則先當工作中，交給工作中的檢查再判斷。
//   - 工作中 → 做完：輸入框正上方是這一輪的結束行、輸入框空、這一輪沒有中斷字樣，連續兩次。
//   - 工作中 → 等你輸入：其他閒置正面證據（Interrupted、啟動畫面、? for shortcuts），連續兩次。
//     窄畫面狀態列會截掉 esc to interrupt、補全清單會蓋住轉圈行，「沒看到在跑」不能當成「停了」。
//   - 工作中 → 等你允許／問你問題：看不到輸入框且有對話框字樣，連續兩次（補上一條沒收到的
//     PermissionRequest，或修正上面單次升級的誤判）。
//
// compare-and-set：抓畫面期間若有新 hook（狀態或 UpdatedAt 變了），以新事件為準、不動。
// 改燈不改 UpdatedAt：它仍代表「最後一個真正的事件」，總覽卡片彙整多個 pane 時，
// 合成出來的狀態不會因為時間較新而蓋掉另一個 pane 真正的事件。
func (s *Store) Observe(p Probe, v ScreenView) Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.byPane[p.Pane]
	if st == nil || st.State != p.State || !st.UpdatedAt.Equal(p.UpdatedAt) {
		return OutcomeKeep
	}
	target, msg, once := "", "", false
	switch p.State {
	case StatePermission, StateQuestion:
		if v.InputBox && !v.Dialog {
			target = StateAfterPrompt(v)
			once = v.Busy
			if target == StateIdle {
				msg = "提示已關閉"
			}
		}
	case StateWorking:
		switch {
		case v.Dialog:
			target, msg = dialogState(v)
		case v.Done:
			// 這一輪正常結束（結束行、輸入框空、沒有中斷字樣）卻沒收到 Stop，或 Stop 之後又來一個
			// 不明 PreToolUse 把燈拉回工作中（QA 實測）：回到「做完」，不把綠燈蓋成等你輸入。
			target, msg = StateDone, ""
		case v.Idle:
			target, msg = StateIdle, "已中斷"
		}
	}
	if target == "" {
		delete(s.misses, p.Pane)
		return OutcomeKeep
	}
	if !once {
		now := s.now()
		m, ok := s.misses[p.Pane]
		if !ok || m.state != p.State || m.target != target || !m.updatedAt.Equal(p.UpdatedAt) {
			s.misses[p.Pane] = miss{state: p.State, target: target, updatedAt: p.UpdatedAt, at: now}
			return OutcomePending
		}
		if now.Sub(m.at) < MinMissGap {
			return OutcomePending
		}
	}
	delete(s.misses, p.Pane)
	c := *st
	c.State, c.Message, c.Event = target, msg, EventScreenCheck
	if target != StatePermission && target != StateQuestion {
		c.Tool = ""
	}
	s.byPane[p.Pane] = &c
	s.persistLocked()
	return OutcomeChanged
}

// dialogState 依對話框種類決定燈號：AskUserQuestion 的問答介面是「問你問題」，其他是「等你允許」。
func dialogState(v ScreenView) (string, string) {
	if v.Question {
		return StateQuestion, "Claude 在問你問題"
	}
	return StatePermission, "等你允許"
}

// SetClock 換掉時間來源（測試用；正式執行一律 time.Now）。
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}
