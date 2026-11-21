package hub

import (
	"context"
	"testing"
	"time"
)

func recv(t *testing.T, c <-chan []byte) string {
	t.Helper()
	select {
	case m := <-c:
		return string(m)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	return ""
}

func TestLatestWinsAndReplayOnSubscribe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := New()
	go h.Run(ctx)
	h.Publish([]byte("s1"))
	time.Sleep(20 * time.Millisecond)
	c, unsub := h.Subscribe()
	if got := recv(t, c); got != "s1" {
		t.Fatalf("新訂閱應先拿到最後快照，got %s", got)
	}
	// 不讀取連發兩份，應只留最新。
	h.Publish([]byte("s2"))
	h.Publish([]byte("s3"))
	time.Sleep(20 * time.Millisecond)
	if got := recv(t, c); got != "s3" {
		t.Fatalf("慢 client 應拿到最新快照，got %s", got)
	}
	unsub()
	if _, ok := <-c; ok {
		t.Fatal("註銷後 channel 應關閉")
	}
}
