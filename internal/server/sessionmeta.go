package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chiyum/remote-console/internal/sessionmeta"
	"github.com/chiyum/remote-console/internal/snapshot"
	"github.com/chiyum/remote-console/internal/tmux"
)

// 卡片 id 只有兩種形狀：tmux session id（$N）與唯讀卡片（proc:<pid>）。格式先擋，再到當下的列表比對存在。
var cardIDRe = regexp.MustCompile(`^(\$[0-9]{1,9}|proc:[0-9]{1,9})$`)

// csrfOK 是所有寫入 API 共用的防 CSRF 檢查：必須 JSON、帶自訂 header（跨站會觸發 preflight 而失敗）、Origin 相符。
func (s *Server) csrfOK(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Dev-Console") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || !s.Auth.OriginOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF 檢查失敗：需 JSON、X-Dev-Console: 1 與相符的 Origin"})
		return false
	}
	return true
}

// decodeStrict 讀小型 JSON body，未知欄位一律拒絕。
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON 格式錯誤"})
		return false
	}
	return true
}

// findCard 驗證 id 格式後，以當下的 tmux／程序表找卡片；找不到回 404。
func (s *Server) findCard(w http.ResponseWriter, r *http.Request, id string) (snapshot.Card, bool) {
	if !cardIDRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session id 格式不合法"})
		return snapshot.Card{}, false
	}
	card, ok := s.Collector.FindCard(r.Context(), id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "找不到這個 session（可能已結束），請重整頁面"})
		return snapshot.Card{}, false
	}
	return card, true
}

func (s *Server) metaEnabled(w http.ResponseWriter) bool {
	if s.Meta == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "暱稱／封存儲存未啟用"})
		return false
	}
	return true
}

// NicknameRequest 是設定暱稱的輸入；Nickname 空字串＝清空。
type NicknameRequest struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
}

// handleNickname 設定或清空卡片暱稱。key 由伺服器依當下卡片決定（Claude 以 session id，--resume 後帶回）。
func (s *Server) handleNickname(w http.ResponseWriter, r *http.Request, id Identity) {
	if !s.csrfOK(w, r) || !s.metaEnabled(w) {
		return
	}
	var req NicknameRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	nick, err := sessionmeta.NormalizeNickname(req.Nickname)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	card, ok := s.findCard(w, r, req.ID)
	if !ok {
		return
	}
	if err := s.Meta.SetNickname(card.NickKey(), nick); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "暱稱寫入失敗"})
		return
	}
	log.Printf("[meta] %s 設定 %s（%s）暱稱 %q", id.Login, card.ID, card.Name, nick)
	// tab 標題由 collector 下一輪同步（立即觸發），與 --resume 回來補設走同一條路。
	s.Collector.Trigger()
	writeJSON(w, http.StatusOK, map[string]string{"nickname": nick})
}

// ArchiveRequest 是封存的輸入（卡片 id）。
type ArchiveRequest struct {
	ID string `json:"id"`
}

// handleArchive 封存卡片：tmux session 先留紀錄再結束（結束失敗就撤回紀錄，避免活著的 session 被藏起來）；
// 唯讀卡片只留紀錄隱藏，絕不碰該程序。
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request, id Identity) {
	if !s.csrfOK(w, r) || !s.metaEnabled(w) {
		return
	}
	var req ArchiveRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	card, ok := s.findCard(w, r, req.ID)
	if !ok {
		return
	}
	rec, err := s.Meta.AddArchive(archiveRecord(card, time.Now()))
	if errors.Is(err, sessionmeta.ErrInvalid) {
		// 唯讀卡片已封存過（卡片被隱藏，但 FindCard 仍找得到）：不重寫紀錄。
		writeJSON(w, http.StatusConflict, map[string]string{"error": "這個 session 已封存"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "封存紀錄寫入失敗"})
		return
	}
	if card.Process == nil {
		// card.ID 來自當下 ListSessions 的 $N（FindCard 驗證過），不是前端字串。
		if err := tmux.KillSessionChecked(r.Context(), s.Tmux, card.ID); err != nil {
			if _, rerr := s.Meta.RemoveArchive(rec.Key); rerr != nil {
				log.Printf("[meta] 撤回封存紀錄 %s 失敗：%v", rec.Key, rerr)
			}
			log.Printf("[meta] %s 封存 %s（%s）時結束 session 失敗：%v", id.Login, card.ID, card.Name, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "結束 tmux session 失敗，未封存"})
			return
		}
	}
	log.Printf("[meta] %s 封存 %s（%s，kind=%s）", id.Login, card.ID, card.Name, rec.Kind)
	s.Collector.Trigger()
	writeJSON(w, http.StatusOK, rec)
}

// archiveRecord 從卡片組出封存紀錄（session 結束後已封存區靠它顯示）。
// tmux 紀錄的 key 加上封存時間：同名 session 可以被封存很多次，各自是一筆歷史，也不會被拿來隱藏同名新 session；
// kill 失敗撤回時也只刪這一筆。唯讀卡片的 key 就是它的隱藏 key（程序還活著時維持隱藏）。
func archiveRecord(card snapshot.Card, now time.Time) sessionmeta.Archive {
	rec := sessionmeta.Archive{
		Key: card.MetaKey + "@" + strconv.FormatInt(now.UnixNano(), 10), Nickname: card.Nickname, Label: card.Name, Tool: card.Tool, Profile: card.Profile,
		Kind: "tmux", SessionID: card.ClaudeSessionID, ResumeCmd: card.ResumeCommand, ArchivedAt: now,
	}
	switch {
	case card.Process != nil:
		rec.Key, rec.Kind, rec.Dir = card.MetaKey, "proc", card.Process.Cwd
	case card.Status != nil && card.Status.Cwd != "":
		rec.Dir = card.Status.Cwd
	case card.ActivePane != nil:
		rec.Dir = card.ActivePane.Path
	}
	return rec
}

// UnarchiveRequest 是取消封存的輸入（封存紀錄的 key）。
type UnarchiveRequest struct {
	Key string `json:"key"`
}

// handleUnarchive 只移除封存紀錄，不重開任何 session。
func (s *Server) handleUnarchive(w http.ResponseWriter, r *http.Request, id Identity) {
	if !s.csrfOK(w, r) || !s.metaEnabled(w) {
		return
	}
	var req UnarchiveRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	if len(req.Key) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key 太長"})
		return
	}
	ok, err := s.Meta.RemoveArchive(req.Key)
	switch {
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "封存紀錄寫入失敗"})
		return
	case !ok:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "沒有這筆封存紀錄"})
		return
	}
	log.Printf("[meta] %s 取消封存 %s", id.Login, req.Key)
	s.Collector.Trigger()
	w.WriteHeader(http.StatusNoContent)
}
