package logthrottle

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLogThrottle(t *testing.T) {
	var out []string
	now := time.Unix(1000, 0)
	lt := New(30 * time.Second)
	lt.Now = func() time.Time { return now }
	lt.Logf = func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }

	for i := 0; i < 50; i++ {
		lt.Printf("k", "失敗 %d", i)
		now = now.Add(200 * time.Millisecond)
	}
	if len(out) != 1 || out[0] != "失敗 0" {
		t.Fatalf("10 秒內 50 次只該記 1 行：%v", out)
	}
	now = now.Add(30 * time.Second)
	lt.Printf("k", "失敗 again")
	if len(out) != 2 || !strings.Contains(out[1], "另有 49 次") {
		t.Fatalf("窗口後應補略過次數：%v", out)
	}
	lt.Printf("other", "別的")
	if len(out) != 3 {
		t.Fatalf("不同 key 各自計：%v", out)
	}
}
