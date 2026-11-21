// dev-console：本機遠端工作台總覽服務。
// 組裝所有依賴（DI 在這裡完成），啟動 UI 與 hook 兩個只綁 loopback 的 listener。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chiyum/remote-console/internal/authstore"
	"github.com/chiyum/remote-console/internal/config"
	"github.com/chiyum/remote-console/internal/hookstore"
	"github.com/chiyum/remote-console/internal/hub"
	"github.com/chiyum/remote-console/internal/procscan"
	"github.com/chiyum/remote-console/internal/server"
	"github.com/chiyum/remote-console/internal/sessionmeta"
	"github.com/chiyum/remote-console/internal/snapshot"
	"github.com/chiyum/remote-console/internal/tmux"
	"github.com/chiyum/remote-console/internal/ttydwatch"
	"github.com/chiyum/remote-console/web"
)

func main() {
	cfg := config.Load()
	if len(os.Args) > 1 {
		os.Exit(runCLI(cfg, os.Args[1:]))
	}
	for _, addr := range []string{cfg.ListenAddr, cfg.HookAddr} {
		if !loopbackAddr(addr) {
			log.Fatalf("監聽位址必須是 loopback：%s", addr)
		}
	}
	if cfg.PublicHost == "" {
		log.Printf("警告：DC_PUBLIC_HOST 未設定，經 tailscale serve 的請求會全部被拒")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := authstore.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("auth store 無法開啟：%v", err)
	}
	runner := tmux.ExecRunner{Path: cfg.TmuxPath}
	h := hub.New()
	hooks := hookstore.New(filepath.Join(cfg.DataDir, "hooks-state.json"))
	collector := snapshot.New(cfg, runner, hooks, h.Publish)
	// 不在 tmux 的既有 aiuse session 以唯讀卡片顯示（ADR 0003）。
	collector.SetProcScanner(procscan.New(cfg.ProfilesRoot))
	// 暱稱與封存紀錄：存在 DataDir（0700）底下的 0600 檔，session 被結束後仍留著（已封存區、--resume 帶回暱稱）。
	meta, err := sessionmeta.Open(filepath.Join(cfg.DataDir, "session-meta.json"))
	if err != nil {
		log.Fatalf("session-meta 無法開啟：%v", err)
	}
	collector.SetMeta(meta)
	// ttyd 監看：fd 過高且沒有任何終端機連線時自動重啟（ADR 0006）。
	ttydGate := &ttydwatch.Gate{}
	ttydWatch := newTtydWatcher(cfg, ttydGate)
	srv := &server.Server{
		Cfg:       cfg,
		Auth:      server.Authorizer{PublicHost: cfg.PublicHost, LocalHost: cfg.LocalHost, ListenAddr: cfg.ListenAddr, AllowedLogins: cfg.AllowedLogins, Sessions: store},
		Hub:       h,
		Collector: collector,
		Hooks:     hooks,
		Launcher:  &server.Launcher{Cfg: cfg, Tmux: runner, Settle: 700 * time.Millisecond},
		Tmux:      runner,
		Static:    web.Static(),
		Store:     store,
		Meta:      meta,
		TtydGate:  ttydGate,
		Ttyd:      ttydWatch,
	}
	go h.Run(ctx)
	go ttydWatch.Run(ctx)
	go collector.Run(ctx)

	ui := &http.Server{Addr: cfg.ListenAddr, Handler: srv.UIHandler(), ReadHeaderTimeout: 10 * time.Second}
	hk := &http.Server{Addr: cfg.HookAddr, Handler: srv.HookHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- ui.ListenAndServe() }()
	go func() { errc <- hk.ListenAndServe() }()
	log.Printf("dev-console 啟動：UI %s、hook %s、ttyd %s、public %s", cfg.ListenAddr, cfg.HookAddr, cfg.TtydSocket, cfg.PublicHost)

	select {
	case err := <-errc:
		// 任一 listener 起不來（例如 port 被占）就整個退出，讓 launchd 重啟並在 log 留下原因。
		log.Fatalf("listener 結束：%v", err)
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = ui.Shutdown(shut)
	_ = hk.Shutdown(shut)
}

// newTtydWatcher 組出正式的 ttyd 監看器（launchctl／lsof／unix socket）；服務與 CLI 共用同一套判斷。
func newTtydWatcher(cfg config.Config, gate *ttydwatch.Gate) *ttydwatch.Watcher {
	return &ttydwatch.Watcher{
		Cfg:       ttydwatch.Config{Interval: cfg.TtydWatchInterval, Threshold: cfg.TtydFDRestart, Limit: cfg.TtydNoFile},
		Gate:      gate,
		Probe:     ttydwatch.Launchd{Label: cfg.TtydLabel, UID: os.Getuid(), Sock: cfg.TtydSocket},
		Restarter: ttydwatch.Launchd{Label: cfg.TtydLabel, UID: os.Getuid(), Sock: cfg.TtydSocket},
	}
}

func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// runCLI 是本機管理子命令；它們只動 DataDir 的 0600 檔，所以必須以本機使用者身分執行。
func runCLI(cfg config.Config, args []string) int {
	switch args[0] {
	case "version":
		fmt.Println("dev-console")
	case "pair":
		code, err := authstore.IssuePairCode(cfg.DataDir, 10*time.Minute)
		if err != nil {
			fmt.Fprintln(os.Stderr, "產生配對碼失敗：", err)
			return 1
		}
		fmt.Printf("配對碼（10 分鐘內、單次有效）：%s\n", code)
		if cfg.PublicHost == "" {
			cfg.PublicHost = tailnetHost()
		}
		_, port, _ := net.SplitHostPort(cfg.ListenAddr)
		if cfg.PublicHost != "" {
			fmt.Printf("手機／其他裝置：https://%s/?pair=%s\n", cfg.PublicHost, code)
		}
		fmt.Printf("本機瀏覽器：http://%s:%s/?pair=%s（本機 cookie 12 小時）\n", cfg.LocalHost, port, code)
		fmt.Println("或在總覽頁的「配對此裝置」輸入配對碼。")
	case "ttyd-status":
		// 給 scripts/check.sh 用：一行狀態，異常時結束碼 1。只讀，不會重啟 ttyd。
		st := newTtydWatcher(cfg, nil).Check(context.Background())
		fmt.Println(st.Line())
		if !st.OK {
			return 1
		}
	case "devices":
		for _, s := range authstore.List(cfg.DataDir) {
			fmt.Printf("%s  %s\n", s.CreatedAt.Format(time.DateTime), s.Label)
		}
	case "revoke-all":
		if err := authstore.RevokeAll(cfg.DataDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("已撤銷所有已配對裝置")
	default:
		fmt.Fprintln(os.Stderr, "用法：dev-console [pair|devices|revoke-all|ttyd-status|version]（不帶參數＝啟動服務）")
		return 2
	}
	return 0
}

// tailnetHost 在手動執行 CLI（沒有 launchd 環境變數）時，向 tailscale 查本機 tailnet 名稱。
func tailnetHost() string {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var st struct {
		Self struct{ DNSName string }
	}
	if json.Unmarshal(out, &st) != nil {
		return ""
	}
	return strings.TrimSuffix(st.Self.DNSName, ".")
}
