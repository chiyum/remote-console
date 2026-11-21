// Package config 集中讀取 dev-console 的執行設定。
// 所有值都從環境變數來（launchd plist 的 EnvironmentVariables 負責注入），
// 讓同一個 binary 換機器時只需改 plist，不必重編。
package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 是服務啟動後不再變動的設定快照。
type Config struct {
	Home string

	// ListenAddr 是總覽頁（含 /term 反代）監聽位址，只能是 loopback，由 tailscale serve 反代。
	ListenAddr string
	// HookAddr 是 Claude hooks 回報專用監聽位址；刻意與 ListenAddr 分開，
	// tailscale serve 只反代 ListenAddr，所以從 tailnet 永遠打不到 hook 端點。
	HookAddr string
	// TtydSocket 是 ttyd 的 unix socket；ttyd 不開 TCP port，只有本服務能連。
	TtydSocket string

	// PublicHost 是 tailscale serve 對外的主機名（例如 my-mac.example-tailnet.ts.net）。
	PublicHost string
	// LocalHost 是本機瀏覽器專用主機名（*.localhost 由瀏覽器解析到 loopback）。cookie 綁這個名稱，
	// 不綁 127.0.0.1——cookie 不分 port，綁 127.0.0.1 會被送到同機其他服務（例如容器開的 :8080）。
	LocalHost string
	// AllowedLogins 是允許經 tailnet 存取的 Tailscale 登入帳號（DC_ALLOWED_LOGINS，逗號分隔）。
	// 預設為空：沒設定時任何帶 Tailscale 登入身分的請求都會被拒，
	// 寧可安裝者忘了填而連不進來，也不要預設放行某個帳號。
	AllowedLogins []string
	ProfilesRoot  string // ~/.ai-profiles
	ClaudeHome    string // ~/.claude（state / acceptance 所在）
	AiusePath     string
	// ClaudeBin／CodexBin 是「直接模式」（沒有 aiuse）開 session 用的執行檔（DC_CLAUDE_BIN／DC_CODEX_BIN）；
	// 空值＝從 SessionPath 找 claude／codex。有 aiuse 時不使用。
	ClaudeBin string
	CodexBin  string
	TmuxPath  string
	// SessionPath 是新開 session 時注入的 PATH；launchd 預設 PATH 太短，
	// 若不帶，claude 內的 Bash 工具會找不到 brew 指令。
	SessionPath string
	Lang        string

	DataDir      string // 服務自身資料（0700）：hook 狀態、配對碼、session 雜湊、hook token
	PollInterval time.Duration

	// ttyd 健康監看與自動重啟（ADR 0006；調法見 README「ttyd 健康與自動恢復」）。
	TtydLabel string // launchd label，查 pid 與 kickstart 用
	// TtydWatchInterval 是偵測週期（DC_TTYD_WATCH_INTERVAL，預設 60s；設 0 關閉自動重啟）。
	TtydWatchInterval time.Duration
	// TtydFDRestart 是 fd 達此數就在沒有連線時重啟 ttyd（DC_TTYD_FD_RESTART，預設 2048）。
	TtydFDRestart int
	// TtydNoFile 是 install.sh 寫進 ttyd plist 的 NumberOfFiles（DC_TTYD_NOFILE，預設 8192），僅供顯示與「快滿」判斷。
	TtydNoFile int
}

// DefaultLabelPrefix 是沒有設定 DC_TTYD_LABEL 時假設的 launchd label 前綴。
// 正式安裝一律由 install.sh 依使用者填的 DC_LABEL_PREFIX 寫進 plist，這裡只是手動執行 CLI 時的後備值。
const DefaultLabelPrefix = "local.dev-console"

// userHomeDir 是 HOME 未設定時的後備（launchd 一定會帶 HOME，這裡只防手動執行時的空值）。
func userHomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}

// Load 從環境變數組出 Config；未設定者採預設值。
func Load() Config {
	home := envOr("HOME", userHomeDir())
	c := Config{
		Home:          home,
		ListenAddr:    envOr("DC_LISTEN_ADDR", "127.0.0.1:7690"),
		HookAddr:      envOr("DC_HOOK_ADDR", "127.0.0.1:7691"),
		TtydSocket:    envOr("DC_TTYD_SOCKET", filepath.Join(home, ".local/share/dev-console/run/ttyd.sock")),
		PublicHost:    strings.TrimSuffix(envOr("DC_PUBLIC_HOST", ""), "."),
		LocalHost:     envOr("DC_LOCAL_HOST", "dev-console.localhost"),
		AllowedLogins: splitList(os.Getenv("DC_ALLOWED_LOGINS")),
		ProfilesRoot:  envOr("DC_PROFILES_ROOT", filepath.Join(home, ".ai-profiles")),
		ClaudeHome:    envOr("DC_CLAUDE_HOME", filepath.Join(home, ".claude")),
		AiusePath:     envOr("DC_AIUSE_PATH", filepath.Join(home, ".local/bin/aiuse")),
		ClaudeBin:     envOr("DC_CLAUDE_BIN", ""),
		CodexBin:      envOr("DC_CODEX_BIN", ""),
		TmuxPath:      envOr("DC_TMUX_PATH", "/opt/homebrew/bin/tmux"),
		SessionPath:   envOr("DC_SESSION_PATH", envOr("PATH", "/usr/bin:/bin")),
		Lang:          envOr("LANG", "en_US.UTF-8"),
		DataDir:       envOr("DC_DATA_DIR", filepath.Join(home, ".local/share/dev-console")),
		PollInterval:  2 * time.Second,
	}
	if d, err := time.ParseDuration(os.Getenv("DC_POLL_INTERVAL")); err == nil && d >= 500*time.Millisecond {
		c.PollInterval = d
	}
	c.TtydLabel = envOr("DC_TTYD_LABEL", DefaultLabelPrefix+".ttyd")
	c.TtydWatchInterval, c.TtydFDRestart, c.TtydNoFile = ttydWatchFromEnv(os.Getenv)
	return c
}

// AiuseAvailable 判斷是否走 aiuse 模式：AiusePath 是存在、可執行的一般檔案（跟隨 symlink）。
// 否則為「直接模式」——網頁新開 session 直接執行 claude／codex、不顯示 profile 選單。
// 每次呼叫都重新檢查（不在啟動時快取）：之後才安裝 aiuse 的機器不必重啟服務就會切回 aiuse 模式。
func (c Config) AiuseAvailable() bool {
	return isExecutableFile(c.AiusePath)
}

// ErrToolNotFound 表示直接模式下找不到 claude／codex 執行檔。
var ErrToolNotFound = errors.New("tool not found")

// ToolBin 回傳直接模式開 tool（claude／codex）要執行的絕對路徑：
// 有設 DC_CLAUDE_BIN／DC_CODEX_BIN 就只認那一個（必須是絕對路徑、可執行）；否則依序在 SessionPath 的絕對目錄找同名檔。
// 用 SessionPath 而不是服務自己的 PATH：session 內看到的 PATH 就是這份，兩邊找到的才會是同一個執行檔。
func (c Config) ToolBin(tool string) (string, error) {
	override := c.ClaudeBin
	if tool == "codex" {
		override = c.CodexBin
	}
	if override != "" {
		if filepath.IsAbs(override) && isExecutableFile(override) {
			return override, nil
		}
		return "", fmt.Errorf("%w: 設定的 %s 執行檔 %s 不存在或不可執行", ErrToolNotFound, tool, override)
	}
	for _, dir := range filepath.SplitList(c.SessionPath) {
		// 相對路徑（含空字串＝目前目錄）不採用：結果會隨服務的工作目錄變動。
		if !filepath.IsAbs(dir) {
			continue
		}
		if p := filepath.Join(dir, tool); isExecutableFile(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: 在 PATH（%s）找不到 %s 指令；請先安裝，或以 DC_%s_BIN 指定絕對路徑", ErrToolNotFound, c.SessionPath, tool, strings.ToUpper(tool))
}

func isExecutableFile(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// ttydWatchFromEnv 讀 ttyd 監看的三個設定；值不合法時用預設並寫 log（install.sh 已先驗過，
// 這裡是手動改 plist 或直接執行 CLI 時的保險，不讓設錯變成靜默套用預設）。
func ttydWatchFromEnv(getenv func(string) string) (interval time.Duration, restart, nofile int) {
	interval = 60 * time.Second
	if v := strings.TrimSpace(getenv("DC_TTYD_WATCH_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			log.Printf("設定 DC_TTYD_WATCH_INTERVAL=%q 不是 Go duration（例：60s），改用預設 %v", v, interval)
		case d <= 0:
			interval = 0
		case d < 5*time.Second:
			// 太短會每幾秒就跑一次 lsof；夾到 5 秒。
			log.Printf("設定 DC_TTYD_WATCH_INTERVAL=%v 太短，改用 5s", d)
			interval = 5 * time.Second
		default:
			interval = d
		}
	}
	restart = envPositive(getenv, "DC_TTYD_FD_RESTART", 2048)
	nofile = envPositive(getenv, "DC_TTYD_NOFILE", 8192)
	if restart >= nofile {
		log.Printf("設定 DC_TTYD_FD_RESTART=%d 不小於 DC_TTYD_NOFILE=%d，自動重啟永遠來不及觸發，請調整", restart, nofile)
	}
	return interval, restart, nofile
}

// envPositive 讀正整數；有設但不合法時寫 log 並用預設。
func envPositive(getenv func(string) string, k string, def int) int {
	v := strings.TrimSpace(getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("設定 %s=%q 不是正整數，改用預設 %d", k, v, def)
		return def
	}
	return n
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
