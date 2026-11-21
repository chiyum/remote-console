// dev-console 終端機頁連線守衛（ADR 0006）。由 Go 反代以「同步 script」注入在 ttyd index 的 </head> 前，
// 早於 ttyd 自己的 script（在 <body>），所以能包住 WebSocket、看到終端機連線的開關。
// 為什麼要有：ttyd 前端斷線後會「立刻」重連、沒有退避，ttyd 掛掉時每秒打好幾次，畫面只有 Reconnecting...。
// ttyd 已用 -t disableReconnect=true 關掉它自己的重連，改由這裡：
//   1. 終端機 WebSocket 不正常關閉（code ≠ 1000；1000＝程序正常結束，維持 ttyd 原本的「Press ⏎」）→ 查 /api/term/health
//   2. 服務異常 → 顯示「終端機服務異常」橫幅，依退避間隔再查（只查健康 API，不打 /term/ws）
//   3. 服務正常 → 失敗次數＋1 後重新載入頁面接回；連線撐過 STABLE_MS 才把失敗次數歸零
// 失敗次數存在 sessionStorage，重新載入後延續退避，連線一直被 ttyd 立刻關掉（例如 fd 用完）也不會變成重載風暴。
// ttyd 連不上時 Go 回的替代頁（<html data-dc-term-down>）也載入本檔，直接進入步驟 2。
"use strict";
(() => {
  const args = new URLSearchParams(location.search).getAll("arg");
  if (args.length !== 1 || !args[0]) return;
  const session = args[0];

  const HEALTH_URL = "/api/term/health";
  const DELAYS_S = [1, 2, 4, 8, 15, 30]; // 第 n 次失敗後等多久（秒），之後固定 30 秒
  const STABLE_MS = 15000;
  const FAIL_KEY = "dc-term-fail:" + session;
  const FETCH_TIMEOUT_MS = 6000;

  const loadFails = () => {
    try {
      return Math.max(0, parseInt(sessionStorage.getItem(FAIL_KEY) || "0", 10) || 0);
    } catch (_) {
      return 0;
    }
  };
  const saveFails = (n) => {
    try {
      if (n > 0) sessionStorage.setItem(FAIL_KEY, String(n));
      else sessionStorage.removeItem(FAIL_KEY);
    } catch (_) {
      // 隱私模式寫不進去：只是重載後退避從頭算，仍有上限。
    }
  };
  let fails = loadFails();
  const delayMs = (n) => DELAYS_S[Math.min(Math.max(n, 1), DELAYS_S.length) - 1] * 1000;

  // ---------- 橫幅 ----------
  let banner = null;
  const showBanner = (text, isError) => {
    const put = () => {
      if (!banner) {
        banner = document.createElement("div");
        banner.id = "dc-term-banner";
        banner.setAttribute("role", "alert");
        document.body.append(banner);
      }
      // ttyd 頁沒有 viewport meta，手機以約 980px 排版再縮小；依縮放倍率放大字級（同 keybar.js）。
      if (matchMedia("(pointer: coarse)").matches && screen.width) {
        const deviceWidth = innerWidth > innerHeight ? Math.max(screen.width, screen.height) : Math.min(screen.width, screen.height);
        const k = Math.min(4, Math.max(1, document.documentElement.clientWidth / deviceWidth));
        banner.style.fontSize = 15 * k + "px";
      }
      banner.textContent = text;
      banner.classList.toggle("dc-term-banner-error", !!isError);
    };
    if (document.body) put();
    else document.addEventListener("DOMContentLoaded", put, { once: true });
  };
  const hideBanner = () => {
    if (banner) banner.remove();
    banner = null;
  };

  // ---------- 健康檢查與退避 ----------
  let timer = 0;
  let checking = false;
  const schedule = (ms) => {
    clearTimeout(timer);
    timer = setTimeout(check, ms);
  };

  const fetchHealth = async () => {
    const ctl = new AbortController();
    const t = setTimeout(() => ctl.abort(), FETCH_TIMEOUT_MS);
    try {
      const res = await fetch(HEALTH_URL, { credentials: "same-origin", cache: "no-store", signal: ctl.signal });
      if (res.status === 403) return { ok: false, fatal: true, reason: "此裝置尚未配對或登入已過期，請回總覽頁重新配對" };
      if (!res.ok) return { ok: false, reason: "dev-console 回應 " + res.status };
      return await res.json();
    } catch (_) {
      return { ok: false, reason: "連不到 dev-console（網路中斷？）" };
    } finally {
      clearTimeout(t);
    }
  };

  async function check() {
    if (checking) return;
    checking = true;
    const st = await fetchHealth();
    checking = false;
    if (st.fatal) {
      showBanner("終端機服務異常：" + st.reason, true);
      return;
    }
    if (st.ok) {
      // 每次重新載入都計一次失敗，撐過 STABLE_MS 才歸零：健康 API 正常但頁面重載後仍拿到替代頁
      // （例如反代改寫 index 失敗）時，退避照樣生效，不會變成每秒重載。
      fails += 1;
      saveFails(fails);
      showBanner(fails > 2 ? "終端機服務已恢復，正在重新連線…" : "正在重新連線…", false);
      location.reload();
      return;
    }
    fails += 1;
    saveFails(fails);
    const wait = delayMs(fails);
    showBanner(`終端機服務異常：${st.reason || "原因不明"}。${Math.round(wait / 1000)} 秒後自動重試，恢復後會自動接回。`, true);
    schedule(wait);
  }

  // 連線不正常結束：先記一次失敗，等退避間隔再查健康（第一次只等 1 秒，提示能在幾秒內出現）。
  const onAbnormalClose = () => {
    fails += 1;
    saveFails(fails);
    const wait = delayMs(fails);
    showBanner(
      fails > 1 ? `終端機服務異常：連線一再中斷，${Math.round(wait / 1000)} 秒後重試。` : "連線中斷，正在檢查終端機服務…",
      fails > 1,
    );
    schedule(wait);
  };

  if (document.documentElement.dataset.dcTermDown) {
    // Go 的替代頁：ttyd 連不上，直接進入退避查詢。
    showBanner("終端機服務異常：ttyd 目前連不上，正在等待恢復…", true);
    schedule(fails > 0 ? delayMs(fails) : 1000);
    return;
  }

  // ---------- 包住 WebSocket，只看 /term/ws ----------
  const Native = window.WebSocket;
  if (typeof Native !== "function") return;
  const isTermWs = (url) => {
    try {
      return new URL(String(url), location.href).pathname === "/term/ws";
    } catch (_) {
      return false;
    }
  };
  // 頁面離開／重新載入時瀏覽器會以 1001 關閉連線，不算失敗。
  let unloading = false;
  addEventListener("pagehide", () => {
    unloading = true;
  });
  class GuardedWebSocket extends Native {
    constructor(url, protocols) {
      super(url, protocols);
      if (!isTermWs(url)) return;
      let stableTimer = 0;
      this.addEventListener("open", () => {
        hideBanner();
        clearTimeout(timer);
        // 撐過 STABLE_MS 才算真的接上，退避歸零（被 ttyd 立刻關掉的連線不算）。
        stableTimer = setTimeout(() => {
          fails = 0;
          saveFails(0);
        }, STABLE_MS);
      });
      this.addEventListener("close", (e) => {
        clearTimeout(stableTimer);
        if (unloading) return;
        if (e.code === 1000) {
          // 程序正常結束（例如 tmux session 結束），沿用 ttyd 的提示，不自動重連。
          fails = 0;
          saveFails(0);
          return;
        }
        onAbnormalClose();
      });
    }
  }
  window.WebSocket = GuardedWebSocket;
})();
