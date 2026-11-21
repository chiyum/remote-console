package hookstore

import (
	"regexp"
	"strings"
)

// 靠 claude TUI 的畫面補 hook 的盲點（純函式，樣本見 testdata/，皆為 claude 2.1.309 在 tmux 內實測）。
//
// hook 不會回報「使用者按 Esc 取消／中斷了」：取消權限提示、取消 AskUserQuestion、工作中中斷，claude 都不送
// 任何 hook。只能看畫面，而畫面判讀以「輸入框」為錨點：
//
//	✶ Catapulting… (28s · ↓ 676 tokens)   ← 轉圈行（工作中才有）
//	  ⎿  ◻ 檢查環境                         ← 任務清單、Tip、effort、tmux 提示等輔助行（略過）
//	────────────────────────────────────    ← 輸入框上框線
//	❯
//	────────────────────────────────────    ← 輸入框下框線
//	  ⏵⏵ accept edits on (shift+tab to  ·  ← 狀態列（窄畫面會截斷，auto 模式甚至整段拿掉 esc to interrupt）
//
// 權限／問答／計畫確認對話框出現時會取代輸入框；ctrl+o 全畫面 transcript 也沒有輸入框。
// 因此：看得到輸入框＝沒有對話框；看不到輸入框又沒有對話框字樣＝判斷不出來，一律維持原燈號。

// dialogTailLines 涵蓋最高的對話框底部：ExitPlanMode 的「Would you like to proceed? → 3 個選項 →
// 回饋提示 → ctrl+g 列」與 Bash 對話框「Do you want to proceed? → 4 個選項 → Esc to cancel」。
const dialogTailLines = 12

// dialogMarkers 是對話框的字樣（已轉小寫）。只在看不到輸入框時才比對，對話內容裡的問句不會讓燈多亮。
var dialogMarkers = []string{
	"esc to cancel",     // 權限對話框、AskUserQuestion 的底部說明列
	"tab to amend",      // 權限對話框底部（進入補充輸入後會消失，但 esc to cancel 仍在）
	"do you want to",    // Bash／Write／Edit／MCP／WebFetch 的問句
	"would you like to", // ExitPlanMode「Would you like to proceed?」
	"enter to select",   // AskUserQuestion「Enter to select · ↑/↓ to navigate · Esc to cancel」
	"1. yes",            // 第一個選項（補充輸入時變成「1. Yes, <輸入的字>」）
}

// maxBelowInputBox 是輸入框下框線之下最多幾個非空行（狀態列、窄畫面折行）。超過就不是目前的輸入框，
// 而是重畫殘影或對話內容裡的舊框線——避免拿它當「輸入框回來了」而把真的提示熄掉。
// 一律以「非空行」計算，高 pane 底部的空白行不影響（與 tailLines 一致）。
const maxBelowInputBox = 6

// transcriptViewMarker 是 ctrl+o 全畫面 transcript 的底部說明列。此時畫面是對話內容（可能引用對話框字樣），
// 不是即時介面，一律判斷不出來。
const transcriptViewMarker = "showing detailed transcript"

var (
	// spinnerRe：工作中的轉圈行「✢ Flowing… (7s · ↓ 128 tokens)」「✻ Churning… (8s · thought for 1s)」。
	// 要求「… (數字+單位」，markdown 項目「* claude (agent) – Catch-all for …」或重畫殘影不會中。
	spinnerRe = regexp.MustCompile(`^[·✢✳✶✻✽*] \S.*… \(\d+[smh]`)
	// doneRe：一輪結束後轉圈行變成「✻ Sautéed for 2s · done 下午10:39」。
	doneRe = regexp.MustCompile(`^[·✢✳✶✻✽*] \S+ for \d.*· done`)
	// todoRe：任務清單行「⎿  ◻ 檢查環境」「◼ …」「✔ …」與標題「5 tasks (0 done, 5 open)」。
	// claude 在「⎿」後面放的是不換行空白 U+00A0，Go regexp 的 \s 只認 ASCII 空白，要明確列入。
	todoRe = regexp.MustCompile(`^(⎿[\s\x{00a0}]*)?([◻◼☐☒✔✓■□]|\d+ tasks \()`)
)

// ScreenView 是一張 pane 畫面的判讀結果。
type ScreenView struct {
	// InputBox：看得到輸入框（上下框線、中間第一行以 ❯ 開頭）。對話框與 ctrl+o transcript 時看不到。
	InputBox bool
	// Dialog：看不到輸入框，而且底部有對話框字樣。
	Dialog bool
	// Question：Dialog 是 AskUserQuestion 的問答介面（Enter to select）。
	Question bool
	// Busy：輸入框上方（略過輔助行）緊接著轉圈行，或狀態列有 esc to interrupt。
	Busy bool
	// Idle：閒置的正面證據——看得到輸入框、不在轉圈，而且輸入框上方是「Interrupted · What should Claude do」、
	// 上一輪的結束行（✻ … for 2s · done）或啟動畫面，或狀態列有「? for shortcuts」。
	// 窄畫面狀態列不可靠（auto 模式工作中也不顯示 esc to interrupt），所以「沒看到忙碌」不等於閒置。
	Idle bool
	// Done：Idle 的一種——輸入框正上方是結束行、輸入框是空的，這一輪是正常結束（不是 Esc 撤回 prompt）。
	Done bool
}

// ReadScreen 判讀 capture-pane 的畫面文字。
func ReadScreen(screen string) ScreenView {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	var v ScreenView
	for _, l := range tailLines(lines, 3) {
		if strings.Contains(strings.ToLower(l), transcriptViewMarker) {
			return v
		}
	}
	top, bottom := findInputBox(lines)
	if top < 0 {
		// 任務清單會畫在對話框下方（實測），不算進底部行數，否則清單一長就把對話框字樣擠出範圍。
		for _, l := range tailLines(withoutTodo(lines), dialogTailLines) {
			n := normalizeLine(l)
			for _, m := range dialogMarkers {
				if strings.Contains(n, m) {
					v.Dialog = true
				}
			}
			if strings.Contains(n, "enter to select") {
				v.Question = true
			}
		}
		return v
	}
	v.InputBox = true
	status := strings.ToLower(strings.Join(lines[bottom+1:], " "))
	if strings.Contains(status, "esc to interrupt") {
		v.Busy = true
	}
	ai := anchorIndex(lines, top)
	anchor := ""
	if ai >= 0 {
		anchor = strings.TrimSpace(lines[ai])
	}
	if spinnerRe.MatchString(anchor) {
		v.Busy = true
	}
	if v.Busy {
		return v
	}
	la := strings.ToLower(anchor)
	v.Idle = strings.Contains(la, "interrupted") || strings.Contains(la, "what should claude do") ||
		doneRe.MatchString(anchor) || bannerLine(anchor) || strings.Contains(status, "? for shortcuts")
	// 做完：結束行、輸入框是空的（Esc 撤回的 prompt 會放回輸入框，那時上方的結束行是上一輪的），
	// 而且這一輪裡沒有中斷／拒絕（取消權限提示後也會畫結束行，但前面有「Interrupted」「User rejected」）。
	v.Done = v.Idle && doneRe.MatchString(anchor) &&
		strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[top+1]), "❯")) == "" &&
		!turnInterrupted(lines, ai)
	return v
}

// StateAfterPrompt 依提示消失後的畫面決定燈號：有閒置正面證據才是等你輸入，其他（在跑、判斷不出來）
// 都先當工作中，交給工作中的畫面檢查再判斷。
func StateAfterPrompt(v ScreenView) string {
	if v.Idle {
		return StateIdle
	}
	return StateWorking
}

// findInputBox 由下往上找輸入框：兩條整行「─」框線，中間第一行以 ❯ 開頭。找不到回傳 -1。
// AskUserQuestion 也有兩條「─」分隔線，但中間第一行是「☐ 標題」，不會被當成輸入框。
func findInputBox(lines []string) (top, bottom int) {
	bottom = -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !borderLine(lines[i]) {
			continue
		}
		if bottom >= 0 && i+1 < bottom && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "❯") {
			return i, bottom
		}
		// 第一條框線，或中間不是輸入列：把這條當成下框線候選。它之下若已超過狀態列的行數，
		// 再往上的框線都不會是目前的輸入框。
		if nonEmptyBelow(lines, i) > maxBelowInputBox {
			return -1, -1
		}
		bottom = i
	}
	return -1, -1
}

// nonEmptyBelow 是第 i 行之後的非空行數（含第 i+1 行起）。
func nonEmptyBelow(lines []string, i int) int {
	n := 0
	for _, l := range lines[i+1:] {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// borderLine：整行（去掉空白後）只有「─」且夠長。權限對話框的「╌」虛線不算。
func borderLine(l string) bool {
	t := strings.TrimSpace(l)
	return len([]rune(t)) >= 10 && strings.Trim(t, "─") == ""
}

// anchorLine 從輸入框上框線往上，略過空行與輔助行，回傳第一個「內容行」（去掉前後空白）。
// anchorIndex 從輸入框上框線往上，略過空行與輔助行，回傳第一個「內容行」的行號（沒有則 -1）。
// 一路找到畫面頂端：高 pane 的新 session 在啟動畫面與輸入框之間可能隔幾十行空白（50 行 pane 實測 41 行），
// 固定行數上限會找不到錨點。
func anchorIndex(lines []string, top int) int {
	for i := top - 1; i >= 0; i-- {
		if !auxLine(lines[i]) {
			return i
		}
	}
	return -1
}

// interruptMarkers 是一輪被使用者中斷或拒絕的字樣（已轉小寫）。
var interruptMarkers = []string{"interrupted", "what should claude do", "user rejected", "user declined"}

// turnInterrupted 由第 end 行往上找到這一輪的使用者 prompt（行首 ❯），看中間有沒有中斷／拒絕字樣。
func turnInterrupted(lines []string, end int) bool {
	for i := end - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "❯") {
			return false
		}
		ll := strings.ToLower(l)
		for _, m := range interruptMarkers {
			if strings.Contains(ll, m) {
				return true
			}
		}
	}
	return false
}

// bannerLine：啟動畫面的 logo／版本列。輸入框正上方就是它，代表這個 session 還沒有任何一輪在跑——
// 送出後很快按 Esc，claude 會把 prompt 撤回、放回輸入框，畫面回到只剩啟動畫面（實測），狀態列也不顯示
// ? for shortcuts，這是唯一的閒置證據。進行中的一輪一定有新的 prompt 行與轉圈行在輸入框上方
// （實測送出後 0.5 秒與 1 秒的畫面都已有 prompt 行與轉圈行，見 testdata/screen-*-fresh-0.5s-after-send.txt）。
// 前提：沒有很慢的 UserPromptSubmit hook。UserPromptSubmit 在 prompt 畫上去之前就會送到並把燈設成工作中，
// 若別的 hook 讓畫面停在啟動畫面超過 workingIdle（1.5 秒）＋兩次確認，燈會被誤降為等你輸入，
// 直到下一個 hook（PreToolUse／Stop）再改回來。
func bannerLine(l string) bool {
	return strings.Contains(l, "Claude Code v") || strings.HasPrefix(l, "▐▛") || strings.HasPrefix(l, "▝▜") || strings.HasPrefix(l, "▝▝")
}

// auxLine 是轉圈行與輸入框之間可能出現的非內容行：
// 空行、靠右對齊的 effort 列（◐ medium · /effort）、tmux 提示、Tip、任務清單與其折行。
func auxLine(l string) bool {
	t := strings.TrimSpace(l)
	if t == "" {
		return true
	}
	indent := len(l) - len(strings.TrimLeft(l, " "))
	switch {
	case indent >= 10, strings.Contains(t, "/effort"), strings.HasPrefix(t, "tmux "):
		return true
	case strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(t, "⎿")), "Tip:"):
		return true
	case todoRe.MatchString(t):
		return true
	case indent >= 5:
		// 任務清單項目在窄畫面的折行（縮排在項目文字之下）。工具輸出的續行也會被略過，
		// 但那之上一定還有「⎿ $ 指令」或轉圈行，不影響判讀。
		return true
	}
	return false
}

// withoutTodo 去掉任務清單行。
func withoutTodo(lines []string) []string {
	res := make([]string, 0, len(lines))
	for _, l := range lines {
		if !todoRe.MatchString(strings.TrimSpace(l)) {
			res = append(res, l)
		}
	}
	return res
}

// tailLines 取最後 n 個非空行（順序由上到下）。
func tailLines(lines []string, n int) []string {
	var res []string
	for i := len(lines) - 1; i >= 0 && len(res) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			res = append(res, lines[i])
		}
	}
	for i, j := 0, len(res)-1; i < j; i, j = i+1, j-1 {
		res[i], res[j] = res[j], res[i]
	}
	return res
}

// normalizeLine 轉小寫並去掉行首的框線、選取游標與空白，讓「 ❯ 1. Yes」「│ Esc to cancel」都能比對。
func normalizeLine(l string) string {
	return strings.ToLower(strings.TrimLeft(l, "│╭╰❯ \t"))
}
