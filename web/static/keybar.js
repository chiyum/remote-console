// dev-console 終端機頁按鍵列（ADR 0005）。由 Go 反代注入到 ttyd 的 index 頁，與 xterm 在同一份 document。
// 手機鍵盤沒有方向鍵／Esc／Tab，Claude 的權限框與選擇題無法操作；點按鈕改由後端 tmux send-keys 送白名單鍵。
// 設計重點：
//   - 不讓任何元素拿到輸入焦點（否則手機鍵盤會彈出、蓋掉畫面）：按鈕 tabindex=-1、pointerdown／mousedown 取消預設，
//     點擊前後若焦點在 xterm 的 textarea 就 blur。
//   - 送出用佇列序列化（前一個請求結束才送下一個），快速連點時順序與次數不變。
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
  ];
  const SEND_TIMEOUT_MS = 8000;

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

  const sendKey = async (key) => {
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), SEND_TIMEOUT_MS);
    try {
      const res = await fetch("/api/sessions/keys", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", "X-Dev-Console": "1" },
        body: JSON.stringify({ session, key }),
        signal: ctl.signal,
      });
      if (!res.ok) {
        let detail = "";
        try {
          detail = (await res.json()).error || "";
        } catch (_) {
          // 非 JSON 回應（例如未配對的 403 純文字）只顯示狀態碼。
        }
        showError(`按鍵沒送出（${res.status}${detail ? "：" + detail : ""}）`);
      }
    } catch (_) {
      showError("按鍵沒送出：連不到 dev-console");
    } finally {
      clearTimeout(timer);
    }
  };

  // 佇列：每個鍵接在前一個請求之後，保證送達順序＝點擊順序。
  let chain = Promise.resolve();
  const enqueue = (key) => {
    chain = chain.then(() => sendKey(key));
  };

  for (const k of KEYS) {
    bar.append(
      makeButton(
        k.label,
        () => {
          // 取消確認時完全不發請求。
          if (k.confirm && !window.confirm(k.confirm)) return;
          enqueue(k.id);
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
