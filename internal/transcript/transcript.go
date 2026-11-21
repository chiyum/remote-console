// Package transcript 增量解析 Claude Code 的 session transcript（JSONL）。
//
// 取數口徑（README 有對應 jq 指令可複驗）：
//   - 只看 type=assistant 且非 sidechain 的行。
//   - Claude 會把同一則回覆的每個 content block 各寫一行，且每行重複同一份 message.usage，
//     所以 token 以 message.id 去重（同 id 取最後一次出現的 usage）後再加總，逐行加總會灌水。
//   - LastText：最後一個 text block；LastTool：最後一個 tool_use block 的 name。
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Usage 是單則回覆的 token 用量。
type Usage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

// Summary 是 transcript 的即時摘要。
type Summary struct {
	LastText string `json:"lastText"`
	LastTool string `json:"lastTool"`
	// 以 message.id 去重後的加總。
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheCreation int64 `json:"cacheCreation"`
	CacheRead     int64 `json:"cacheRead"`
	// Context 是最後一則回覆的 input+cache_creation+cache_read，約等於目前上下文大小。
	Context   int64     `json:"context"`
	Messages  int       `json:"messages"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Accumulator 保存增量解析的中間狀態；同一個檔案只往後讀，不重讀。
type Accumulator struct {
	usage    map[string]Usage
	lastText string
	lastTool string
	lastID   string
}

// NewAccumulator 建立空的累加器。
func NewAccumulator() *Accumulator { return &Accumulator{usage: map[string]Usage{}} }

type line struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
		Usage   *Usage          `json:"usage"`
	} `json:"message"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

var assistantMarker = []byte(`"type":"assistant"`)

// AddLine 吃一行 JSONL；非 assistant 行用位元組比對快速略過，避免解析大型 request blob。
func (a *Accumulator) AddLine(b []byte) {
	if !bytes.Contains(b, assistantMarker) {
		return
	}
	var l line
	if json.Unmarshal(b, &l) != nil || l.Type != "assistant" || l.IsSidechain {
		return
	}
	if l.Message.Usage != nil && l.Message.ID != "" {
		a.usage[l.Message.ID] = *l.Message.Usage
		a.lastID = l.Message.ID
	}
	var blocks []block
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return
	}
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			if bl.Text != "" {
				a.lastText = bl.Text
			}
		case "tool_use":
			if bl.Name != "" {
				a.lastTool = bl.Name
			}
		}
	}
}

// Summary 匯出目前摘要。
func (a *Accumulator) Summary() Summary {
	s := Summary{LastText: a.lastText, LastTool: a.lastTool, Messages: len(a.usage)}
	for _, u := range a.usage {
		s.Input += u.Input
		s.Output += u.Output
		s.CacheCreation += u.CacheCreation
		s.CacheRead += u.CacheRead
	}
	if u, ok := a.usage[a.lastID]; ok {
		s.Context = u.Input + u.CacheCreation + u.CacheRead
	}
	return s
}

// Parse 一次解析整個 reader（測試與一次性呼叫用）。
func Parse(r io.Reader) Summary {
	a := NewAccumulator()
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		b, err := br.ReadBytes('\n')
		if len(b) > 0 && b[len(b)-1] == '\n' {
			a.AddLine(b)
		} else if len(b) > 0 && err == io.EOF {
			a.AddLine(b)
		}
		if err != nil {
			break
		}
	}
	return a.Summary()
}

type fileState struct {
	acc     *Accumulator
	offset  int64
	partial []byte
	modTime time.Time
	summary Summary
}

// Tracker 依檔案路徑保存增量狀態；檔案被截短（size < offset）就從頭重算。
type Tracker struct {
	mu    sync.Mutex
	files map[string]*fileState
}

// NewTracker 建立 Tracker。
func NewTracker() *Tracker { return &Tracker{files: map[string]*fileState{}} }

// Get 讀取 path 自上次之後新增的內容並回傳最新摘要。
func (t *Tracker) Get(path string) (Summary, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fi, err := os.Stat(path)
	if err != nil {
		return Summary{}, err
	}
	st := t.files[path]
	if st == nil || fi.Size() < st.offset {
		st = &fileState{acc: NewAccumulator()}
		t.files[path] = st
	}
	if fi.Size() == st.offset && fi.ModTime().Equal(st.modTime) {
		return st.summary, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()
	if _, err := f.Seek(st.offset, io.SeekStart); err != nil {
		return Summary{}, err
	}
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		b, err := br.ReadBytes('\n')
		st.offset += int64(len(b))
		if len(b) > 0 && b[len(b)-1] == '\n' {
			// 有上一輪殘留的半行就接起來再解析。
			if len(st.partial) > 0 {
				b = append(st.partial, b...)
				st.partial = nil
			}
			st.acc.AddLine(b)
		} else if len(b) > 0 {
			// 尚未寫完的最後一行先暫存，下一輪補齊後再解析。
			st.partial = append(st.partial, b...)
		}
		if err != nil {
			break
		}
	}
	st.modTime = fi.ModTime()
	st.summary = st.acc.Summary()
	st.summary.UpdatedAt = fi.ModTime()
	return st.summary, nil
}

// Invalidate 讓下一次 Get 不走「大小與 mtime 都沒變」的快取（收到 Stop 時呼叫）。
// 只清快取判斷，不丟已解析的累計值，仍從原位移往後讀。
func (t *Tracker) Invalidate(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.files[path]; st != nil {
		st.modTime = time.Time{}
	}
}

// Forget 移除不再追蹤的檔案，避免 map 無限長大。
func (t *Tracker) Forget(keep map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for p := range t.files {
		if !keep[p] {
			delete(t.files, p)
		}
	}
}
