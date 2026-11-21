// dev-console 總覽頁：用 SSE 接收完整快照並重畫。所有文字一律走 textContent，避免 transcript 內容造成 XSS。
"use strict";

const $ = (sel) => document.querySelector(sel);

const LIGHT_TEXT = {
  waiting_permission: "等你允許",
  question: "問你問題",
  working: "工作中",
  done: "做完",
  idle: "等你輸入",
  ended: "已結束",
};
const STATUS_TEXT = { awaiting_user: "等你決定", running: "進行中", reported: "已回報", awaiting_next_batch: "等下一批", done: "完成" };

/** 建立元素的小工具：el("div", {class: "x"}, child...)；字串子節點一律當純文字。 */
function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else n.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    n.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return n;
}

function ago(ts) {
  if (!ts || ts.startsWith("0001")) return "—";
  const s = Math.max(0, Math.round((Date.now() - new Date(ts).getTime()) / 1000));
  if (s < 60) return `${s} 秒前`;
  if (s < 3600) return `${Math.floor(s / 60)} 分前`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小時前`;
  return `${Math.floor(s / 86400)} 天前`;
}

const fmtTime = (ts) => (ts && !ts.startsWith("0001") ? new Date(ts).toLocaleString("zh-TW", { hour12: false }) : "—");

/** 不在 tmux 的 session 怎麼接手：只能在原終端機結束後以 resume 重開（重開時 aiuse 會自動進 tmux）。
 *  指令本身由後端 resumeCommand 組（封存紀錄的接手指令同一份），前端只負責包說明文字。 */
function resumeHint(c) {
  const p = c.process;
  if (!c.resumeCommand) return `接手：在原終端機（${p.tty}）結束後，以 aiuse resume 重開，重開就會進 tmux，之後可從網頁操作。`;
  const pick = p.tool === "codex" ? " 選回這段對話" : "";
  return `接手：在原終端機（${p.tty}）結束後，執行 ${c.resumeCommand}${pick}，重開就會進 tmux，之後可從網頁操作。`;
}

/** 寫入 API 共用：JSON＋X-Dev-Console（伺服器另檢查 Origin），錯誤訊息取後端回的 error。 */
async function postJSON(url, body) {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Dev-Console": "1" },
    body: JSON.stringify(body),
  });
  const data = res.status === 204 ? {} : await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

const NICKNAME_MAX = 40; // 與後端 sessionmeta.MaxNicknameRunes 一致

/** 改暱稱：用 prompt，因為卡片每次收到快照就重畫，內嵌輸入框會被洗掉。空白＝清空。 */
async function editNickname(c) {
  const input = window.prompt(`「${c.name}」的暱稱（留空＝清除，最多 ${NICKNAME_MAX} 字）`, c.nickname || "");
  if (input === null) return;
  const nick = input.trim();
  if ([...nick].length > NICKNAME_MAX) {
    window.alert(`暱稱最多 ${NICKNAME_MAX} 個字，未儲存`);
    return;
  }
  try {
    await postJSON("/api/sessions/nickname", { id: c.id, nickname: nick });
  } catch (err) {
    window.alert(`暱稱未儲存：${err.message}`);
  }
}

/** 封存前的確認文字依卡片種類而不同：唯讀卡片只隱藏；aiuse session 可用接手指令找回；普通 shell 找不回。 */
function archiveConfirmText(c) {
  const title = c.nickname || c.name;
  if (c.process) return `封存「${title}」？\n\n只隱藏、不結束程序：卡片從總覽頁消失，程序仍在原終端機執行。`;
  if (c.resumeCommand) return `封存「${title}」？\n\n會結束這個 tmux session。之後可用已封存區的接手指令找回：\n${c.resumeCommand}`;
  return `封存「${title}」？\n\n會結束這個 tmux session。這個 session 無法找回（不是 aiuse 開的 Claude／Codex）。`;
}

async function archiveCard(c) {
  if (!window.confirm(archiveConfirmText(c))) return;
  try {
    await postJSON("/api/sessions/archive", { id: c.id });
  } catch (err) {
    window.alert(`封存失敗：${err.message}`);
  }
}

/** 卡片底部：tmux session 給「開終端機」；不在 tmux 的唯讀卡片絕不給連結，避免接到別的 session。 */
function cardActions(c) {
  const archiveBtn = el("button", { type: "button", class: "btn", "data-role": "archive" }, "封存");
  archiveBtn.addEventListener("click", () => archiveCard(c));
  if (c.process) return el("div", { class: "card-actions" }, el("span", { class: "muted small", "data-role": "resume-hint" }, resumeHint(c)), archiveBtn);
  return el(
    "div",
    { class: "card-actions" },
    c.termUrl ? el("a", { class: "btn primary", href: c.termUrl }, "開終端機") : el("span", { class: "muted small" }, "名稱含特殊字元，無法從網頁開啟"),
    archiveBtn,
  );
}

/** 卡片標題：有暱稱時暱稱當主標題、tmux 名稱縮成小字；鉛筆按鈕改暱稱。 */
function cardTitle(c) {
  const edit = el("button", { type: "button", class: "icon-btn", "data-role": "edit-nickname", "aria-label": "改暱稱", title: "改暱稱" }, "✎");
  edit.addEventListener("click", () => editNickname(c));
  return [
    el("span", { class: "name", "data-role": "title" }, c.nickname || c.name),
    c.nickname ? el("span", { class: "muted small mono", "data-role": "tmux-name" }, c.name) : null,
    edit,
  ];
}

const fmtNum = (n) => (n >= 1e6 ? `${(n / 1e6).toFixed(2)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}k` : String(n || 0));

function renderSessions(sessions) {
  const box = $("#sessions");
  $("#session-count").textContent = `（${sessions.length}）`;
  if (!sessions.length) {
    box.replaceChildren(el("div", { class: "empty" }, "目前沒有 session"));
    return;
  }
  // 需要你處理的排最前面，其次依最近活動。
  const rank = (c) => ({ waiting_permission: 0, question: 1 }[c.status?.state] ?? 2);
  sessions.sort((a, b) => rank(a) - rank(b) || new Date(b.activity) - new Date(a.activity));
  box.replaceChildren(
    ...sessions.map((c) => {
      const st = c.status;
      const alert = st && (st.state === "waiting_permission" || st.state === "question");
      const t = c.transcript;
      const tool = st && st.state === "working" && st.tool ? `當前 tool：${st.tool}` : t?.lastTool ? `最後 tool：${t.lastTool}` : null;
      return el(
        "article",
        { class: `card${alert ? " alert" : ""}${c.process ? " readonly" : ""}`, "data-session": c.name },
        el(
          "div",
          { class: "card-head" },
          st ? el("span", { class: `light light-${st.state}` }, LIGHT_TEXT[st.state] || st.state) : null,
          cardTitle(c),
        ),
        el(
          "div",
          { class: "meta" },
          c.tool ? el("span", { class: "badge" }, c.tool) : null,
          c.profile ? el("span", { class: "badge" }, c.profile) : null,
          c.process
            ? [el("span", null, `終端機 ${c.process.tty}`), el("span", null, `PID ${c.process.pid}`), el("span", null, `啟動 ${fmtTime(c.process.started)}`)]
            : [el("span", null, c.attached ? `${c.attached} 個畫面連線中` : "未 attach"), el("span", null, `活動 ${ago(c.activity)}`)],
        ),
        c.process ? el("div", { class: "readonly-note", "data-role": "readonly" }, "不在 tmux（只能看，不能從網頁操作）") : null,
        c.process?.cwd ? el("div", { class: "muted small mono" }, c.process.cwd) : null,
        st?.message ? el("div", { class: "muted small" }, st.message) : null,
        t?.lastText ? el("div", { class: "reply", "data-role": "last-reply" }, t.lastText) : null,
        tool || t
          ? el(
              "div",
              { class: "meta" },
              tool ? el("span", { "data-role": "tool" }, tool) : null,
              t
                ? el(
                    "span",
                    { "data-role": "tokens", title: "以 message.id 去重後加總；上下文＝最後一則的 input+cache" },
                    `token 輸入 ${fmtNum(t.input)}・輸出 ${fmtNum(t.output)}・快取寫 ${fmtNum(t.cacheCreation)}・快取讀 ${fmtNum(t.cacheRead)}・上下文 ${fmtNum(t.context)}`,
                  )
                : null,
            )
          : null,
        c.activePane ? el("div", { class: "muted small mono" }, `${c.activePane.command} @ ${c.activePane.path}`) : null,
        cardActions(c),
      );
    }),
  );
}

// 手機上一眼看到「誰在等」：進行中／等你決定／等下一批，以及 14 天內有更新的任務直接展開；其餘收進摺疊區。
const ACTIVE_STATUS = new Set(["running", "awaiting_user", "awaiting_next_batch"]);
const RECENT_MS = 14 * 86400 * 1000;
const isPrimaryTask = (t) => ACTIVE_STATUS.has(t.status) || Date.now() - new Date(t.updatedAt).getTime() < RECENT_MS;

function renderTasks(tasks) {
  const primary = tasks.filter(isPrimaryTask);
  const older = tasks.filter((t) => !isPrimaryTask(t));
  $("#task-count").textContent = `（${primary.length}）`;
  fillTaskList($("#tasks"), primary, "沒有進行中或近期的任務");
  $("#tasks-old-summary").textContent = `較舊／已完成（${older.length}）`;
  $("#tasks-old-wrap").hidden = older.length === 0;
  fillTaskList($("#tasks-old"), older, "");
}

function fillTaskList(box, tasks, emptyText) {
  if (!tasks.length) {
    box.replaceChildren(emptyText ? el("div", { class: "empty" }, emptyText) : "");
    return;
  }
  box.replaceChildren(
    ...tasks.map((t) =>
      el(
        "div",
        { class: `task${t.status === "awaiting_user" ? " awaiting" : ""}`, "data-task": t.task, "data-status": t.status },
        el(
          "div",
          { class: "task-head" },
          el("span", { class: `badge status-${t.status}` }, STATUS_TEXT[t.status] ? `${STATUS_TEXT[t.status]}（${t.status}）` : t.status || "?"),
          el("strong", null, t.task),
          t.progress ? el("span", { class: "progress", "data-role": "progress" }, `驗收 ${t.progress.verified}/${t.progress.items}`) : null,
          el("span", { class: "muted small" }, ago(t.updatedAt)),
        ),
        t.nextAction ? el("div", { class: "small" }, t.nextAction) : null,
      ),
    ),
  );
}

/** 複製到剪貼簿：http://dev-console.localhost 與 https tailnet 都是安全來源，可用 Clipboard API；失敗再退回選取複製。 */
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = el("textarea", { readonly: "", class: "copy-buffer" });
    ta.value = text;
    document.body.append(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  }
}

const TOOL_TEXT = { claude: "Claude", codex: "Codex" };

function renderArchived(list) {
  $("#archived-summary").textContent = `已封存（${list.length}）`;
  const box = $("#archived");
  if (!list.length) {
    box.replaceChildren(el("div", { class: "empty" }, "沒有已封存的 session"));
    return;
  }
  box.replaceChildren(
    ...list.map((a) => {
      const actions = el("div", { class: "card-actions" });
      if (a.resumeCommand) {
        const copy = el("button", { type: "button", class: "btn", "data-role": "copy-resume" }, "複製");
        copy.addEventListener("click", async () => {
          copy.textContent = (await copyText(a.resumeCommand)) ? "已複製" : "複製失敗";
        });
        actions.append(copy);
      }
      const un = el("button", { type: "button", class: "btn", "data-role": "unarchive" }, "取消封存");
      un.addEventListener("click", async () => {
        try {
          await postJSON("/api/sessions/unarchive", { key: a.key });
        } catch (err) {
          window.alert(`取消封存失敗：${err.message}`);
        }
      });
      actions.append(un);
      return el(
        "div",
        { class: "task archived-item", "data-archived": a.key },
        el(
          "div",
          { class: "task-head" },
          el("strong", { "data-role": "title" }, a.nickname || a.label),
          a.nickname ? el("span", { class: "muted small mono" }, a.label) : null,
        ),
        el(
          "div",
          { class: "meta" },
          a.tool ? el("span", { class: "badge" }, TOOL_TEXT[a.tool] || a.tool) : null,
          a.profile ? el("span", { class: "badge" }, a.profile) : null,
          el("span", null, `封存 ${fmtTime(a.archivedAt)}`),
          a.kind === "proc"
            ? el("span", { "data-role": "proc-state" }, a.alive ? "只隱藏（程序仍在執行）" : "程序已結束")
            : null,
        ),
        a.dir ? el("div", { class: "muted small mono", "data-role": "dir" }, a.dir) : null,
        a.resumeCommand
          ? el("code", { class: "mono resume-cmd", "data-role": "resume-command" }, a.resumeCommand)
          : el("div", { class: "muted small" }, "這個 session 無法找回"),
        actions,
      );
    }),
  );
}

function renderWatchdog(w) {
  $("#watchdog").replaceChildren(
    el("div", null, `最後一行（${ago(w.lastLineAt)}）：`, w.lastLine || "—"),
    el("div", null, `.last-sweep：${w.lastSweepAt ? new Date(w.lastSweepAt).toLocaleString() : "—"}（${ago(w.lastSweepAt)}）`),
  );
}

let lastSnap = null;
function render(snap) {
  lastSnap = snap;
  renderSessions(snap.sessions || []);
  renderTasks(snap.tasks || []);
  renderWatchdog(snap.watchdog || {});
  renderArchived(snap.archived || []);
  const errs = snap.errors || [];
  $("#errors").hidden = errs.length === 0;
  $("#errors").textContent = errs.join("\n");
  $("#generated").textContent = `快照 ${new Date(snap.generatedAt).toLocaleTimeString()}`;
}

function setConn(ok, text) {
  const c = $("#conn");
  c.className = `conn ${ok ? "conn-ok" : "conn-wait"}`;
  c.textContent = text;
}

// SSE：瀏覽器會自動重連；另設 60 秒看門狗，太久沒收到（含 20 秒保活）就主動重建連線。
let es = null;
let lastBeat = Date.now();
function connect() {
  if (es) es.close();
  es = new EventSource("/api/events");
  es.addEventListener("snapshot", (e) => {
    lastBeat = Date.now();
    setConn(true, "即時");
    try {
      render(JSON.parse(e.data));
    } catch (err) {
      console.error(err);
    }
  });
  es.onopen = () => {
    lastBeat = Date.now();
    setConn(true, "即時");
  };
  es.onerror = () => setConn(false, "重連中…");
}
setInterval(() => {
  if (Date.now() - lastBeat > 60000) connect();
  // 相對時間每 15 秒重算一次，不必等新快照。
  if (lastSnap) render(lastSnap);
}, 15000);

// ---- 終端機服務（ttyd）狀態（ADR 0006）----
// 每 30 秒查一次；異常時標紅並帶原因，fd 數讓人看得出是否在漏。
async function refreshTtyd() {
  const box = $("#ttyd");
  try {
    const r = await fetch("/api/term/health", { cache: "no-store" });
    if (!r.ok) throw new Error(String(r.status));
    const st = await r.json();
    box.hidden = false;
    // warning＝fd 超過自動重啟門檻但仍可用（等沒人連線時自動重啟），標橘不標紅。
    box.className = `conn ${!st.ok ? "conn-bad" : st.warning ? "conn-wait" : "conn-ok"}`;
    const fd = st.pid ? `fd ${st.fds}／${st.limit}` : "";
    box.textContent = !st.ok
      ? `終端機服務異常：${st.reason}${fd ? `（${fd}）` : ""}`
      : `終端機 OK（${fd}）${st.warning ? `：${st.warning}` : ""}`;
    box.title = `ttyd pid ${st.pid || "—"}，自動重啟門檻 fd ${st.threshold}，連線 ${st.activeConns}`;
  } catch (_) {
    box.hidden = false;
    box.className = "conn conn-wait";
    box.textContent = "終端機狀態未知";
  }
}

// ---- 新開 session ----
// mode：aiuse＝多 profile（顯示 profile 選單）；direct＝這台沒有 aiuse，直接開 claude／codex，不顯示 profile 選單。
let profiles = { mode: "aiuse", claude: [], codex: [] };
const form = $("#launch-form");
const isDirect = () => profiles.mode === "direct";

function fillProfiles() {
  const tool = form.tool.value;
  const sel = $("#profile-select");
  // 直接模式整列隱藏並取消必填，否則空的 select 會擋住送出。
  $("#profile-row").hidden = isDirect();
  sel.required = !isDirect();
  sel.replaceChildren(...(isDirect() ? [] : profiles[tool] || []).map((p) => el("option", { value: p }, p)));
}

$("#launch-toggle").addEventListener("click", () => {
  form.hidden = !form.hidden;
  $("#launch-toggle").setAttribute("aria-expanded", String(!form.hidden));
});
form.addEventListener("change", (e) => {
  if (e.target.name === "tool") fillProfiles();
});
form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const btn = $("#launch-submit");
  const msg = $("#launch-msg");
  msg.textContent = "";
  btn.disabled = true;
  btn.textContent = "建立中…";
  try {
    const body = { tool: form.tool.value, profile: isDirect() ? "" : form.profile.value, dir: $("#dir-details").open ? form.dir.value.trim() : "" };
    const res = await fetch("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Dev-Console": "1" },
      body: JSON.stringify(body),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
    location.href = data.termUrl;
  } catch (err) {
    msg.textContent = err.message;
    btn.disabled = false;
    btn.textContent = "開始";
  }
});

// 配對：POST 表單（伺服器檢查 Origin），成功後伺服器設 HttpOnly cookie，前端重新載入。
async function pair(code) {
  const res = await fetch("/auth/pair", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ code }),
  });
  if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
}

$("#pair-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("#pair-msg").textContent = "";
  $("#pair-submit").disabled = true;
  try {
    await pair($("#pair-code").value);
    location.replace("/");
  } catch (err) {
    $("#pair-msg").textContent = err.message;
    $("#pair-submit").disabled = false;
  }
});

async function init() {
  // `dev-console pair` 印出的網址是 /?pair=<碼>：自動 POST 兌換，並立刻把碼從網址列移除。
  const params = new URLSearchParams(location.search);
  const urlCode = params.get("pair");
  if (urlCode) {
    history.replaceState(null, "", "/");
    try {
      await pair(urlCode);
    } catch (err) {
      $("#pair-msg").textContent = `網址中的配對碼無效：${err.message}`;
    }
  }
  try {
    const who = await fetch("/api/whoami").then((r) => (r.ok ? r.json() : null));
    if (!who) {
      // 未配對：只顯示配對表單，不連 SSE（伺服器也會拒絕）。表單以 GET 送到 /auth/pair，成功後伺服器設 cookie 並導回首頁。
      $("#who").textContent = "未配對";
      $("#pair-panel").hidden = false;
      $("#app-body").hidden = true;
      setConn(false, "未配對");
      return;
    }
    $("#who").textContent = `${who.login}（${who.via}）`;
    const p = await fetch("/api/profiles").then((r) => (r.ok ? r.json() : null));
    if (p) profiles = { mode: p.mode || "aiuse", claude: p.claude || [], codex: p.codex || [] };
    fillProfiles();
    refreshTtyd();
    setInterval(refreshTtyd, 30000);
  } catch (err) {
    console.error(err);
  }
  connect();
}
init();
