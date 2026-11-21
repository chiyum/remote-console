#!/bin/bash
# 在 tailnet policy 維持一條最小規則：<擁有者帳號> → <本機 tailnet IP>:22,443。
# 為什麼需要：本機掛 tag（例如 tag:workstation）後就不屬於 autogroup:self，個人裝置（手機）預設連不到它。
# 來源只給擁有者本人（不是 autogroup:member），日後 tailnet 加人也不會自動連得到這台。
#
# 用法：TS_API_TOKEN=<admin api token> scripts/tailscale-acl.sh --src <你的 Tailscale 帳號 email> [--ip 100.x.y.z] [--ports 22,443] [--dry-run]
#   - --src 必填（也可用環境變數 DC_ACL_SRC）：來源帳號因人而異，不給預設值。
#   - --ip 不給就取本機 tailscale ip -4（也可用環境變數 DC_ACL_DST_IP）。
#   - 同一個目的地（IP:ports）的其他 accept 規則（例如舊版的 autogroup:member）會被替換成這一條；重跑冪等。
#   - token 只從環境變數讀，絕不寫進檔案；輸出不含 token。
#   - 流程：GET（取 ETag）→ 已有同一條就跳過 → /acl/validate → 帶 If-Match POST（他人同時改過會 412，不會蓋掉）。
#   - policy 若含註解就中止：本腳本以 JSON 往返，會吃掉 HuJSON 註解。
#   - 可重跑；換機器時用 --ip 指定新 IP。
set -euo pipefail
IP="${DC_ACL_DST_IP:-}"
PORTS="22,443"
SRC="${DC_ACL_SRC:-}"
DRY=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --ip) IP="$2"; shift 2 ;;
    --ports) PORTS="$2"; shift 2 ;;
    --src) SRC="$2"; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    *) echo "未知參數：$1" >&2; exit 1 ;;
  esac
done
: "${TS_API_TOKEN:?請以環境變數 TS_API_TOKEN 提供 Tailscale Admin API token}"
[ -n "$IP" ] || IP="$(tailscale ip -4 | head -1)"
[[ "$IP" =~ ^100\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "IP 不像 tailnet IPv4：$IP" >&2; exit 1; }
[[ "$PORTS" =~ ^[0-9]+(,[0-9]+)*$ ]] || { echo "ports 格式錯：${PORTS}" >&2; exit 1; }
[ -n "$SRC" ] || { echo "請以 --src 或 DC_ACL_SRC 提供來源帳號 email" >&2; exit 1; }
[[ "$SRC" =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$ ]] || { echo "src 必須是單一帳號 email：${SRC}" >&2; exit 1; }

API="https://api.tailscale.com/api/v2/tailnet/-/acl"
# 結束碼保護：macOS 內建 /bin/bash 3.2 在 set -u 觸發「未設定變數」而中止時，若有 EXIT trap，
# 整支腳本會以 trap 最後一個指令的狀態碼結束（通常是 0），trap 內的 $? 也讀到 0——中途失敗會被當成成功。
# 所以用「完成旗標」：只有走到正常結束點才標記完成；沒標記就一律以非 0 結束（原本就非 0 的保留原值）。
ACL_DONE=0
WORK="$(mktemp -d)"
on_exit() {
  local rc=$?
  rm -rf "${WORK:?}"
  if [ "$ACL_DONE" != "1" ]; then
    [ "$rc" -ne 0 ] || rc=1
    exit "$rc"
  fi
}
trap on_exit EXIT
api() { curl -sS -u "${TS_API_TOKEN}:" "$@"; }

echo "== GET policy（HuJSON，檢查註解）"
api -H 'Accept: application/hujson' -o "$WORK/policy.hujson" -w 'HTTP %{http_code}\n' "$API"
if grep -Eq '^[[:space:]]*//|/\*' "$WORK/policy.hujson"; then
  echo "policy 含註解，JSON 往返會遺失註解；請改到 admin console 手動加規則。" >&2
  exit 2
fi
echo "== GET policy（JSON，取 ETag）"
api -H 'Accept: application/json' -D "$WORK/get.headers" -o "$WORK/policy.json" -w 'HTTP %{http_code}\n' "$API"
ETAG="$(grep -i '^etag:' "$WORK/get.headers" | sed -E 's/^[Ee][Tt][Aa][Gg]: *//; s/\r$//')"
[ -n "$ETAG" ] || { echo "取不到 ETag" >&2; exit 1; }
echo "ETag: $ETAG"

RULE="$(jq -nc --arg dst "$IP:$PORTS" --arg src "$SRC" '{action:"accept",src:[$src],dst:[$dst]}')"
echo "目標規則：$RULE"
# 新 policy＝拿掉「目的地完全相同、但不是目標規則」的 accept 規則，再確保目標規則恰好一條。
jq --argjson r "$RULE" '
  .acls = ([(.acls // [])[] | select((.action == "accept" and .dst == $r.dst) | not)] + [$r])
' "$WORK/policy.json" > "$WORK/new.json"
if [ "$(jq -S -c . "$WORK/policy.json")" = "$(jq -S -c . "$WORK/new.json")" ]; then
  echo "規則已是目標狀態，跳過（未修改 policy）。"
  jq -c --arg ip "$IP" '.acls[] | select(any(.dst[]; startswith($ip + ":")))' "$WORK/policy.json"
  ACL_DONE=1
  exit 0
fi
echo "將替換的舊規則（同目的地）："
jq -c --argjson r "$RULE" '.acls[] | select(.action == "accept" and .dst == $r.dst and . != $r)' "$WORK/policy.json"
echo "== 差異（新 policy 相對於現行）"
diff <(jq -S . "$WORK/policy.json") <(jq -S . "$WORK/new.json") || true

echo "== POST /acl/validate"
api -X POST -H 'Content-Type: application/json' --data-binary @"$WORK/new.json" -o "$WORK/validate.out" -w 'HTTP %{http_code}\n' "$API/validate"
cat "$WORK/validate.out"; echo
# validate 成功時回 {} 或空內容；有 message 代表錯誤。
if jq -e '.message? // empty' "$WORK/validate.out" >/dev/null 2>&1; then
  echo "validate 失敗，中止。" >&2
  exit 1
fi
if [ "$DRY" = "1" ]; then
  echo "--dry-run：不送出。"
  ACL_DONE=1
  exit 0
fi
echo "== POST policy（If-Match: ${ETAG}）"
code="$(api -X POST -H 'Content-Type: application/json' -H "If-Match: $ETAG" --data-binary @"$WORK/new.json" -o "$WORK/post.out" -w '%{http_code}' "$API")"
echo "HTTP $code"
if [ "$code" != "200" ]; then
  cat "$WORK/post.out" >&2
  exit 1
fi
echo "== 套用後的 acls"
jq -c '.acls[]' "$WORK/post.out"
ACL_DONE=1
