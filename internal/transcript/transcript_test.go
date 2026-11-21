package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 模擬 Claude 真實寫法：同一 message.id 拆成多行（thinking / text / tool_use），每行帶同一份 usage。
const sample = `{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","message":{"id":"m1","content":[{"type":"thinking","thinking":"..."}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000}}}
{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"第一段回覆"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000}}}
{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","name":"Bash","input":{}}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000}}}
{"type":"assistant","isSidechain":true,"message":{"id":"side","content":[{"type":"text","text":"子代理不算"}],"usage":{"input_tokens":999,"output_tokens":999}}}
{"type":"assistant","message":{"id":"m2","content":[{"type":"tool_use","name":"Read","input":{}}],"usage":{"input_tokens":2,"output_tokens":7,"cache_creation_input_tokens":20,"cache_read_input_tokens":1100}}}
{"type":"assistant","message":{"id":"m3","content":[{"type":"text","text":"最後回覆"}],"usage":{"input_tokens":3,"output_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":1200}}}
not-json "type":"assistant"
`

func TestParseDedupByMessageID(t *testing.T) {
	s := Parse(strings.NewReader(sample))
	if s.LastText != "最後回覆" || s.LastTool != "Read" {
		t.Fatalf("text/tool 錯：%+v", s)
	}
	if s.Messages != 3 || s.Input != 15 || s.Output != 21 || s.CacheCreation != 120 || s.CacheRead != 3300 {
		t.Fatalf("token 加總錯（應以 message.id 去重）：%+v", s)
	}
	if s.Context != 1203 {
		t.Fatalf("context=%d", s.Context)
	}
}

func TestTrackerIncrementalAndPartialLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	lines := strings.SplitAfter(sample, "\n")
	// 先寫前三行＋第四行的一半，模擬 claude 正在寫入。
	half := len(lines[3]) / 2
	_ = os.WriteFile(p, []byte(strings.Join(lines[:3], "")+lines[3][:half]), 0o644)
	tr := NewTracker()
	s, err := tr.Get(p)
	if err != nil || s.LastText != "第一段回覆" || s.LastTool != "" {
		t.Fatalf("第一輪：%+v %v", s, err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(lines[3][half:] + strings.Join(lines[4:], ""))
	_ = f.Close()
	s, _ = tr.Get(p)
	full := Parse(strings.NewReader(sample))
	full.UpdatedAt = s.UpdatedAt
	if s != full {
		t.Fatalf("增量結果應與一次解析相同：\n%+v\n%+v", s, full)
	}
	// 截短後應重算。
	_ = os.WriteFile(p, []byte(lines[0]+lines[2]), 0o644)
	s, _ = tr.Get(p)
	if s.Messages != 1 || s.LastText != "第一段回覆" {
		t.Fatalf("截短後應重算：%+v", s)
	}
}
