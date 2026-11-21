// Package tasks 唯讀讀取 ~/.claude/state 的任務檢查點與看門狗心跳。
// 只用 os.ReadFile / ReadDir / Stat，絕不寫入；總覽頁是觀察者，不參與狀態機。
package tasks

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chiyum/remote-console/internal/progress"
)

// Task 是一個 state 檔的摘要。
type Task struct {
	File       string             `json:"file"`
	Task       string             `json:"task"`
	Status     string             `json:"status"`
	NextAction string             `json:"nextAction"`
	Acceptance string             `json:"acceptance,omitempty"`
	Progress   *progress.Progress `json:"progress,omitempty"`
	UpdatedAt  time.Time          `json:"updatedAt"`
}

// Watchdog 是看門狗心跳：watchdog.log 最後一行時間與 .last-sweep 的 mtime。
type Watchdog struct {
	LastLine    string     `json:"lastLine"`
	LastLineAt  *time.Time `json:"lastLineAt,omitempty"`
	LastSweepAt *time.Time `json:"lastSweepAt,omitempty"`
}

// statusRank 決定排序：需要使用者決定的排最前面。
var statusRank = map[string]int{"awaiting_user": 0, "running": 1, "awaiting_next_batch": 2, "reported": 3}

// LoadTasks 讀 stateDir/*.json，回傳任務與「解析失敗的檔名」（只回報、不修）。
// 全部列出，摺疊與篩選交給前端（不改資料來源）。
func LoadTasks(stateDir, claudeHome string) ([]Task, []string) {
	files, _ := filepath.Glob(filepath.Join(stateDir, "*.json"))
	var out []Task
	var bad []string
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			bad = append(bad, filepath.Base(f)+"："+err.Error())
			continue
		}
		t := Task{
			File:       filepath.Base(f),
			Task:       str(raw["task"]),
			Status:     str(raw["status"]),
			NextAction: str(raw["next_action"]),
			Acceptance: str(raw["acceptance"]),
			UpdatedAt:  fi.ModTime(),
		}
		if t.Task == "" {
			t.Task = strings.TrimSuffix(t.File, ".json")
		}
		if t.Acceptance != "" {
			t.Progress = progress.Load(progress.ResolvePath(claudeHome, t.Acceptance))
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i].Status), rank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, bad
}

func rank(s string) int {
	if r, ok := statusRank[s]; ok {
		return r
	}
	return 9
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

var tsRe = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]`)

// LoadWatchdog 讀 watchdog.log 最後一行（只讀尾端 8KB）與 .last-sweep mtime。
func LoadWatchdog(stateDir string, loc *time.Location) Watchdog {
	var w Watchdog
	if line := lastLine(filepath.Join(stateDir, "watchdog.log")); line != "" {
		w.LastLine = line
		if m := tsRe.FindStringSubmatch(line); m != nil {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], loc); err == nil {
				w.LastLineAt = &t
			}
		}
	}
	if fi, err := os.Stat(filepath.Join(stateDir, ".last-sweep")); err == nil {
		t := fi.ModTime()
		w.LastSweepAt = &t
	}
	return w
}

// LastLineOf 回傳 reader 內容的最後一個非空行（給測試與 lastLine 共用）。
func LastLineOf(b []byte) string {
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(string(lines[len(lines)-1]))
}

func lastLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	const tail = 8 << 10
	off := fi.Size() - tail
	if off < 0 {
		off = 0
	}
	b, err := io.ReadAll(io.NewSectionReader(f, off, fi.Size()-off))
	if err != nil {
		return ""
	}
	return LastLineOf(b)
}
