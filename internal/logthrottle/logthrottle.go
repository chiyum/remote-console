// Package logthrottle 讓同一種訊息在一個窗口內只寫一次，窗口結束後下一次記錄時補「期間又發生 N 次」。
// 用在會被高頻觸發的失敗訊息（例如 ttyd 掛掉時前端密集重試、健康檢查每次都讀不到 fd），避免把 log 洗掉。
package logthrottle

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// Throttle 以 key 分類節流；零值不可用，請用 New。
type Throttle struct {
	mu     sync.Mutex
	window time.Duration
	// Now 與 Logf 可在測試替換。
	Now   func() time.Time
	Logf  func(format string, args ...any)
	state map[string]*entry
}

type entry struct {
	last       time.Time
	suppressed int
}

// New 建立窗口為 window 的節流器，預設寫到標準 log。
func New(window time.Duration) *Throttle {
	return &Throttle{window: window, Now: time.Now, Logf: log.Printf, state: map[string]*entry{}}
}

// Printf 窗口內重複的 key 只計數不寫。
func (t *Throttle) Printf(key, format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	e := t.state[key]
	if e != nil && now.Sub(e.last) < t.window {
		e.suppressed++
		return
	}
	msg := fmt.Sprintf(format, args...)
	if e != nil && e.suppressed > 0 {
		msg += fmt.Sprintf("（前 %v 內另有 %d 次相同狀況未記錄）", t.window, e.suppressed)
	}
	t.state[key] = &entry{last: now}
	t.Logf("%s", msg)
}
