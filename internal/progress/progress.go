// Package progress 計算驗收清單（~/.claude/acceptance/<任務>.md）的進度：
//   - 條目＝清單內不重複的 `### <大寫字母><數字>`（A1、B3…）。
//   - 已驗＝evidence/ 第一層「一般檔案」（跟隨 symlink）中，檔名含 `<條目>-` 的條目數。
//     相當於 glob `*A1-*`，所以 `qa-A3-x.png`、`pm-A12-y.txt` 都算；
//     另外要求條目前一個字元不是英數，避免 `DATA10-` 被算成 A10（單純的 glob 會誤配）。
package progress

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var itemRe = regexp.MustCompile(`(?m)^### ([A-Z][0-9]+)`)

// Progress 是一份驗收清單的進度。
type Progress struct {
	Path     string `json:"path"`
	Items    int    `json:"items"`
	Verified int    `json:"verified"`
}

// ItemIDs 回傳清單內不重複的條目 ID（保持出現順序）。
func ItemIDs(md []byte) []string {
	seen := map[string]bool{}
	var ids []string
	for _, m := range itemRe.FindAllSubmatch(md, -1) {
		id := string(m[1])
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// CountItems 計算不重複條目數。
func CountItems(md []byte) int { return len(ItemIDs(md)) }

// HasEvidence 判斷檔名是否為該條目的證據：含 `<id>-`，且 id 前一個字元不是英數（或在開頭）。
func HasEvidence(name, id string) bool {
	needle := id + "-"
	for off := 0; ; {
		i := strings.Index(name[off:], needle)
		if i < 0 {
			return false
		}
		pos := off + i
		if pos == 0 || !isAlnum(name[pos-1]) {
			return true
		}
		off = pos + 1
	}
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// CountVerified 計算有證據檔的條目數；目錄不存在視為 0。
func CountVerified(dir string, ids []string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var names []string
	for _, e := range entries {
		// 對齊 find -L -type f：跟隨 symlink，只算一般檔案。
		if fi, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && fi.Mode().IsRegular() {
			names = append(names, e.Name())
		}
	}
	n := 0
	for _, id := range ids {
		for _, name := range names {
			if HasEvidence(name, id) {
				n++
				break
			}
		}
	}
	return n
}

// ResolvePath 把 state 檔裡的 acceptance 欄位轉成絕對路徑；相對路徑以 ~/.claude 為基準
// （state 檔常寫成 "acceptance/xxx.md"）。
func ResolvePath(claudeHome, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(claudeHome, p)
}

// EvidenceDir 由清單路徑推出 evidence 目錄：X.md → X/evidence。
func EvidenceDir(mdPath string) string {
	return filepath.Join(strings.TrimSuffix(mdPath, ".md"), "evidence")
}

// Load 讀清單並計算進度；讀不到清單回 nil。
func Load(mdPath string) *Progress {
	b, err := os.ReadFile(mdPath)
	if err != nil {
		return nil
	}
	ids := ItemIDs(b)
	return &Progress{Path: mdPath, Items: len(ids), Verified: CountVerified(EvidenceDir(mdPath), ids)}
}
