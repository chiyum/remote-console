// Package hub 把總覽快照推給所有 SSE 連線。
//
// 設計（避免多 goroutine 共用 map 的競態，以及慢連線拖住廣播）：
//   - clients map 只由 Run() 這一個 goroutine 讀寫，註冊／註銷／廣播都走 channel。
//   - 每個 client 的 channel 容量 1，訊息是「完整快照」，滿了就丟舊的換新的（最新者勝），
//     被丟的只是過時快照，下一筆就補齊；丟棄有計數 log，不是靜默。
//   - 新連線註冊時立即補送最後一份快照，斷線重連的瀏覽器不會看到空白。
package hub

import (
	"context"
	"log"
)

// Hub 是快照廣播器。
type Hub struct {
	register   chan chan []byte
	unregister chan chan []byte
	publish    chan []byte
}

// New 建立 Hub；需另呼叫 Run。
func New() *Hub {
	return &Hub{
		register:   make(chan chan []byte),
		unregister: make(chan chan []byte),
		publish:    make(chan []byte, 4),
	}
}

// Run 是唯一持有 clients map 的 goroutine。
func (h *Hub) Run(ctx context.Context) {
	clients := map[chan []byte]struct{}{}
	var last []byte
	dropped := 0
	for {
		select {
		case <-ctx.Done():
			for c := range clients {
				close(c)
			}
			return
		case c := <-h.register:
			clients[c] = struct{}{}
			if last != nil {
				c <- last // 容量 1 的新 channel，必定不阻塞
			}
		case c := <-h.unregister:
			if _, ok := clients[c]; ok {
				delete(clients, c)
				close(c)
			}
		case msg := <-h.publish:
			last = msg
			for c := range clients {
				select {
				case c <- msg:
				default:
					// 慢 client：丟掉還沒被讀走的舊快照，換成最新一份。
					select {
					case <-c:
					default:
					}
					select {
					case c <- msg:
					default:
					}
					dropped++
					if dropped%50 == 1 {
						log.Printf("[hub] 慢 client 快照被覆蓋（累計 %d 次）", dropped)
					}
				}
			}
		}
	}
}

// Subscribe 註冊一個新 client，回傳接收 channel 與取消函式。
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	c := make(chan []byte, 1)
	h.register <- c
	return c, func() { h.unregister <- c }
}

// Publish 送出新快照；publish 有小 buffer，滿了就丟（下一輪 poll 會再送）。
func (h *Hub) Publish(msg []byte) {
	select {
	case h.publish <- msg:
	default:
		log.Printf("[hub] publish 佇列滿，略過一份快照（下一輪會補）")
	}
}
