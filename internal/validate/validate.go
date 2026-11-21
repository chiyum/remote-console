// Package validate 集中所有「從網頁進來、最後會變成 tmux / aiuse 參數」的輸入檢查。
// 原則：白名單＋存在性驗證，任何不確定一律拒絕；呼叫端只用 exec 陣列，不拼 shell 字串。
package validate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// 首字必須是英數：否則 "--help" 之類名稱會被 aiuse／tmux 當成選項。
	// 與 aiuse 接受的 profile 名稱同一套規則（英數、底線、連字號，首字不可是 -，對應 aiuse 的 `-*` 拒絕規則）。大小寫照磁碟原樣：
	// macOS 檔案系統不分大小寫，所以 Profile 另外要求名稱是目錄裡原樣存在的那個（見 dev-console ADR 0003）。
	profileRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)
	// sessionRe 限制可經網頁 attach 的 tmux session 名稱；tmux 本身會把 . 與 : 換掉，
	// 這裡再收窄成英數、底線、連字號，讓 ttyd wrapper 與 Go 端用同一套規則。
	sessionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,119}$`)
	paneRe    = regexp.MustCompile(`^%[0-9]{1,9}$`)
)

// 不是 profile 的保留目錄（claude/shared 是共用設定，不是帳號）。
var reservedProfiles = map[string]bool{"shared": true}

// ErrInvalid 標記「使用者輸入錯誤」，handler 依此回 400。
var ErrInvalid = errors.New("invalid input")

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Tool 只接受 claude 與 codex。
func Tool(tool string) error {
	if tool != "claude" && tool != "codex" {
		return invalidf("工具只能是 claude 或 codex")
	}
	return nil
}

// Profile 驗證 profile 名稱格式，且必須是 <root>/<tool>/ 底下實際存在的目錄（不跟隨 symlink）。
func Profile(root, tool, name string) error {
	if err := Tool(tool); err != nil {
		return err
	}
	if !profileRe.MatchString(name) || reservedProfiles[strings.ToLower(name)] {
		return invalidf("profile 名稱不合法")
	}
	fi, err := os.Lstat(filepath.Join(root, tool, name))
	if err != nil || !fi.IsDir() {
		return invalidf("profile 不存在：%s/%s", tool, name)
	}
	// 不分大小寫的檔案系統上 Lstat("WORK") 也會成功；只接受目錄原樣的名稱，避免同一個帳號以兩種寫法進來。
	for _, p := range ListProfiles(root, tool) {
		if p == name {
			return nil
		}
	}
	return invalidf("profile 不存在：%s/%s", tool, name)
}

// ListProfiles 列出 <root>/<tool>/ 底下合法的 profile 名稱（與 Profile 同一套規則）。
func ListProfiles(root, tool string) []string {
	entries, err := os.ReadDir(filepath.Join(root, tool))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && profileRe.MatchString(e.Name()) && !reservedProfiles[strings.ToLower(e.Name())] {
			out = append(out, e.Name())
		}
	}
	return out
}

// Dir 把使用者輸入的工作目錄解析成實際路徑：空字串與 "~" 代表 home，"~/x" 展開；
// 解析 symlink 後必須是存在的目錄且位於 home 之下，避免把 session 開到 /tmp 或系統目錄。
func Dir(home, input string) (string, error) {
	input = strings.TrimSpace(input)
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("home 無法解析：%w", err)
	}
	switch {
	case input == "" || input == "~":
		return realHome, nil
	case strings.HasPrefix(input, "~/"):
		input = filepath.Join(home, input[2:])
	case !filepath.IsAbs(input):
		return "", invalidf("目錄必須是絕對路徑或 ~/ 開頭")
	}
	if strings.ContainsAny(input, "\x00\n\r") {
		return "", invalidf("目錄含不合法字元")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(input))
	if err != nil {
		return "", invalidf("目錄不存在")
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return "", invalidf("不是目錄")
	}
	if resolved != realHome && !strings.HasPrefix(resolved, realHome+string(os.PathSeparator)) {
		return "", invalidf("目錄必須在 home 之下")
	}
	return resolved, nil
}

// SessionName 檢查可經網頁 attach 的 tmux session 名稱。
func SessionName(name string) bool { return sessionRe.MatchString(name) }

// PaneID 檢查 tmux pane id（hook 帶來的 $TMUX_PANE），格式 %數字。
func PaneID(id string) bool { return paneRe.MatchString(id) }

// ProfileName 只檢查格式（hook 回報用，不做存在性檢查）。
func ProfileName(name string) bool { return profileRe.MatchString(name) }
