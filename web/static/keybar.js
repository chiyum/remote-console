// dev-console 終端機頁按鍵列（ADR 0005）。由 Go 反代注入到 ttyd 的 index 頁，與 xterm 在同一份 document。
// 手機鍵盤沒有方向鍵／Esc／Tab，Claude 的權限框與選擇題無法操作；點按鈕改由後端 tmux send-keys 送白名單鍵。
// 設計重點：
//   - 不讓任何元素拿到輸入焦點（否則手機鍵盤會彈出、蓋掉畫面）：按鈕 tabindex=-1、pointerdown／mousedown 取消預設，
//     點擊前後若焦點在 xterm 的 textarea 就 blur。
//   - 送出用佇列序列化（前一個請求結束才送下一個），快速連點時順序與次數不變。
//   - ▲ 上捲／▼ 下捲與手指上下滑（ADR 0009）：Claude／Codex 是全螢幕程式，tmux 沒有 scrollback、手機觸控也不會產生滾輪，
//     所以改由後端依 pane 狀態送翻頁鍵或滾輪序列。手指往下拖＝看更早內容；位移沒超過門檻視為輕點，照常聚焦叫出鍵盤；
//     多指（縮放）不處理；沒有慣性，手指放開後只把已經滑過的列數送完。桌機滑鼠滾輪與拖曳選字不經過這裡。
//   - 展開時把 #terminal-container 縮成「視窗高 − 按鍵列高」再呼叫 window.term.fit()，xterm 重算列數、
//     ttyd 送 resize 給 tmux，最底一行不會被蓋住。
"use strict";
(() => {
  const KEYS = [
    { id: "esc", label: "Esc" },
    { id: "tab", label: "Tab" },
    { id: "btab", label: "⇧Tab" },
    { id: "up", label: "↑" },
    { id: "down", label: "↓" },
    { id: "left", label: "←" },
    { id: "right", label: "→" },
    { id: "enter", label: "Enter" },
    { id: "1", label: "1" },
    { id: "2", label: "2" },
    { id: "3", label: "3" },
    { id: "4", label: "4" },
    { id: "ctrl-c", label: "Ctrl+C", confirm: "確定要送出 Ctrl+C 嗎？（會中斷目前的工作）", danger: true },
    { scroll: "page-up", label: "▲ 上捲" },
    { scroll: "page-down", label: "▼ 下捲" },
  ];
  const SEND_TIMEOUT_MS = 8000;
  // 逐列捲動一次請求的上限，與後端 tmux.MaxScrollLines 一致；滑得更遠就分多次送。
  const MAX_SCROLL_LINES = 30;
  // 手指位移超過這麼多「裝置像素」才算滑動，否則當作輕點（手指點擊時的自然抖動通常在幾個像素內）。
  const SWIPE_START_DEVICE_PX = 10;

  const args = new URLSearchParams(location.search).getAll("arg");
  if (args.length !== 1 || !args[0]) return;
  const session = args[0];

  const coarse = matchMedia("(pointer: coarse)").matches;
  // 觸控與桌機各記各的展開狀態；沒記過時觸控預設展開、桌機預設收起。
  const storeKey = "dc-keybar-open:" + (coarse ? "touch" : "desktop");
  const loadOpen = () => {
    try {
      const v = localStorage.getItem(storeKey);
      if (v === "1") return true;
      if (v === "0") return false;
    } catch (_) {
      // 隱私模式等情況 localStorage 會丟例外，退回預設值。
    }
    return coarse;
  };
  const saveOpen = (v) => {
    try {
      localStorage.setItem(storeKey, v ? "1" : "0");
    } catch (_) {
      // 記不住只影響下次預設值，不影響本次操作。
    }
  };

  /** 焦點在可輸入元素上就移開，避免手機鍵盤彈出。 */
  const blurEditable = () => {
    const a = document.activeElement;
    if (a && (a.tagName === "TEXTAREA" || a.tagName === "INPUT" || a.isContentEditable)) a.blur();
  };

  const html = document.documentElement;
  const bar = document.createElement("div");
  bar.id = "dc-keybar";
  bar.setAttribute("role", "toolbar");
  bar.setAttribute("aria-label", "終端機按鍵");
  const msg = document.createElement("div");
  msg.id = "dc-kb-msg";
  const toggle = document.createElement("button");
  toggle.id = "dc-keybar-toggle";
  toggle.type = "button";
  toggle.textContent = "⌨ 按鍵";
  toggle.title = "展開按鍵列";

  /** 建一顆不會取得焦點的按鈕；動作在 click 執行（pointerdown 只負責擋焦點）。 */
  const makeButton = (label, onClick, extraClass) => {
    const b = document.createElement("button");
    b.type = "button";
    b.tabIndex = -1;
    b.textContent = label;
    if (extraClass) b.className = extraClass;
    b.addEventListener("click", (e) => {
      e.preventDefault();
      blurEditable();
      onClick();
      if (document.activeElement === b) b.blur();
    });
    return b;
  };
  for (const n of [bar, toggle]) {
    // 取消預設以免按鈕或 xterm 取得焦點；touchstart 不能取消（會連 click 一起吃掉）。
    for (const ev of ["pointerdown", "mousedown"]) {
      n.addEventListener(ev, (e) => {
        e.preventDefault();
        blurEditable();
      });
    }
  }

  let msgTimer = 0;
  const showError = (text) => {
    msg.textContent = text;
    bar.classList.add("dc-kb-has-msg");
    clearTimeout(msgTimer);
    msgTimer = setTimeout(() => {
      msg.textContent = "";
      bar.classList.remove("dc-kb-has-msg");
      layout();
    }, 4000);
    layout();
  };

  /** 送一個寫入請求；成功回 true。失敗時在按鍵列顯示原因。 */
  const post = async (path, body, what) => {
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), SEND_TIMEOUT_MS);
    try {
      const res = await fetch(path, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", "X-Dev-Console": "1" },
        body: JSON.stringify(Object.assign({ session }, body)),
        signal: ctl.signal,
      });
      if (!res.ok) {
        let detail = "";
        try {
          detail = (await res.json()).error || "";
        } catch (_) {
          // 非 JSON 回應（例如未配對的 403 純文字）只顯示狀態碼。
        }
        showError(`${what}沒送出（${res.status}${detail ? "：" + detail : ""}）`);
        return false;
      }
      return true;
    } catch (_) {
      showError(`${what}沒送出：連不到 dev-console`);
      return false;
    } finally {
      clearTimeout(timer);
    }
  };

  // 佇列：每個鍵接在前一個請求之後，保證送達順序＝點擊順序（捲動鍵與手指滑動也排在同一條佇列）。
  let chain = Promise.resolve();
  const enqueue = (task) => {
    chain = chain.then(task);
  };
  const sendKey = (key) => enqueue(() => post("/api/sessions/keys", { key }, "按鍵"));
  const sendScroll = (action, count) => enqueue(() => post("/api/sessions/scroll", count ? { action, count } : { action }, "捲動"));

  for (const k of KEYS) {
    bar.append(
      makeButton(
        k.label,
        () => {
          // 取消確認時完全不發請求。
          if (k.confirm && !window.confirm(k.confirm)) return;
          if (k.scroll) sendScroll(k.scroll);
          else sendKey(k.id);
        },
        k.danger ? "dc-kb-danger" : "",
      ),
    );
  }
  bar.append(makeButton("收起", () => setOpen(false), "dc-kb-hide"), msg);

  // 沒有 viewport meta 的 ttyd 頁在手機上以 980px 寬排版再縮小顯示，按鈕依縮放倍率放大，維持手指點得到的大小。
  const scale = () => {
    if (!coarse) return 1;
    const lo = Math.min(screen.width, screen.height);
    const hi = Math.max(screen.width, screen.height);
    const deviceWidth = innerWidth > innerHeight ? hi : lo;
    if (!deviceWidth) return 1;
    return Math.min(4, Math.max(1, html.clientWidth / deviceWidth));
  };

  let fitTries = 0;
  const fit = () => {
    requestAnimationFrame(() => {
      if (window.term && typeof window.term.fit === "function") {
        window.term.fit();
        fitTries = 0;
      } else if (fitTries++ < 20) {
        // ttyd 還沒建好 xterm，稍後再試（它自己的初次 fit 可能用的是縮小前的高度）。
        setTimeout(fit, 100);
      }
    });
  };

  let open = false;
  const layout = () => {
    const px = 15 * scale();
    bar.style.fontSize = px + "px";
    toggle.style.fontSize = px + "px";
    const h = open ? Math.ceil(bar.getBoundingClientRect().height) : 0;
    html.style.setProperty("--dc-kb-h", h + "px");
    fit();
  };
  const setOpen = (v) => {
    open = v;
    html.classList.toggle("dc-kb-open", v);
    toggle.title = v ? "收起按鍵列" : "展開按鍵列";
    saveOpen(v);
    layout();
  };
  toggle.addEventListener("click", (e) => {
    e.preventDefault();
    blurEditable();
    setOpen(true);
  });

  // ---- 手指上下滑捲動 ----
  // 待送的列數：正＝往上（看更早），負＝往下。滑動期間只累加，佇列空出來時合併成一個請求送出，
  // 手指移得比請求快也不會堆出一長串請求。
  let pendingLines = 0;
  let flushQueued = false;
  const flushScroll = () => {
    if (flushQueued || pendingLines === 0) return;
    flushQueued = true;
    enqueue(async () => {
      flushQueued = false;
      const n = Math.max(-MAX_SCROLL_LINES, Math.min(MAX_SCROLL_LINES, pendingLines));
      if (n === 0) return;
      pendingLines -= n;
      const ok = await post("/api/sessions/scroll", { action: n > 0 ? "line-up" : "line-down", count: Math.abs(n) }, "捲動");
      // 失敗（例如畫面不支援）就丟掉剩下的列數，不重複送、不重複跳錯誤。
      if (!ok) pendingLines = 0;
      flushScroll();
    });
  };
  /** 一列在畫面上的高度（CSS px）：手指移動一列高就捲一列，內容跟著手指走。 */
  const linePx = () => {
    const t = window.term;
    const screenEl = document.querySelector("#terminal-container .xterm-screen");
    if (t && t.rows > 0 && screenEl) {
      const h = screenEl.getBoundingClientRect().height / t.rows;
      if (h > 2) return h;
    }
    return 16;
  };
  /** 一個裝置像素等於幾個 CSS px：沒有 viewport meta 的頁面在手機上是縮小顯示（約 980px 排版），還可能被雙指縮放過。 */
  const cssPerDevicePx = () => {
    const vv = window.visualViewport;
    if (vv && vv.scale > 0) return 1 / vv.scale;
    return scale();
  };
  let swipe = null;
  const inTerminal = (el) => !!(el && el.closest && el.closest("#terminal-container"));
  document.addEventListener(
    "touchstart",
    (e) => {
      swipe = null;
      // 多指（縮放）不處理；只管終端機區（按鍵列不在 #terminal-container 內）。
      if (e.touches.length !== 1 || !inTerminal(e.target)) return;
      const p = e.touches[0];
      swipe = { x0: p.clientX, y0: p.clientY, base: 0, active: false, sent: 0, line: linePx(), threshold: SWIPE_START_DEVICE_PX * cssPerDevicePx() };
    },
    { capture: true, passive: true },
  );
  document.addEventListener(
    "touchmove",
    (e) => {
      if (!swipe) return;
      if (e.touches.length !== 1) {
        swipe = null;
        return;
      }
      const p = e.touches[0];
      if (!swipe.active) {
        const dx = p.clientX - swipe.x0;
        const dy = p.clientY - swipe.y0;
        if (Math.abs(dy) < swipe.threshold) return;
        // 偏橫向的拖動留給瀏覽器（例如放大後左右平移）。
        if (Math.abs(dx) > Math.abs(dy)) {
          swipe = null;
          return;
        }
        // 從越過門檻的位置起算，門檻內的位移不算列數，避免一開始就跳一段。
        swipe.active = true;
        swipe.base = p.clientY;
      }
      // 已經在捲動：不讓瀏覽器平移頁面，也不讓 xterm 自己處理這次觸控。
      e.preventDefault();
      e.stopPropagation();
      const lines = Math.trunc((p.clientY - swipe.base) / swipe.line);
      if (lines !== swipe.sent) {
        pendingLines += lines - swipe.sent;
        swipe.sent = lines;
        flushScroll();
      }
    },
    { capture: true, passive: false },
  );
  const endSwipe = (e) => {
    // 真的捲動過才取消預設（避免放開時被當成點擊）；輕點完全不攔，xterm 照常聚焦。
    if (swipe && swipe.active && e.cancelable) e.preventDefault();
    swipe = null;
  };
  document.addEventListener("touchend", endSwipe, { capture: true, passive: false });
  document.addEventListener("touchcancel", endSwipe, { capture: true, passive: false });

  document.body.append(bar, toggle);
  open = loadOpen();
  html.classList.toggle("dc-kb-open", open);
  layout();
  // 轉向、視窗改大小、按鍵列換行數改變時重算高度並重新 fit。
  addEventListener("resize", layout);
  if (window.ResizeObserver) {
    let last = -1;
    new ResizeObserver(() => {
      const h = Math.ceil(bar.getBoundingClientRect().height);
      if (h !== last) {
        last = h;
        if (open) layout();
      }
    }).observe(bar);
  }
})();
