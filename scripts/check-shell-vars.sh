#!/bin/bash
# 靜態檢查：shell 腳本裡「$變數」後面直接接非 ASCII 字元（例如 `$NAME` 後面緊接全形括號或全形冒號）。
# macOS 內建的 /bin/bash 3.2 在 UTF-8 語系會把緊接的多位元組字元併進變數名，set -u 下變成「未設定變數」而中途中止
# （install.sh 曾因此在 aiuse 模式安裝到一半停下）。一律寫成 ${變數}。註解也一起檢查，避免複製時把壞寫法帶進程式。
# 用 LC_ALL=C 的 grep 逐位元組比對：[^ -~[:space:]] 在 C 語系只會命中非 ASCII 位元組，macOS 內建 grep 也可用（不需要 -P）。
# 用法：scripts/check-shell-vars.sh [檔案…]（不給檔案＝install.sh、uninstall.sh、scripts/*.sh）；有命中時列出並以 1 結束。
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -eq 0 ]; then
  set -- install.sh uninstall.sh scripts/*.sh
fi
if LC_ALL=C grep -nE '\$[A-Za-z_][A-Za-z0-9_]*[^ -~[:space:]]' "$@"; then
  echo '上列 $變數 後面直接接非 ASCII 字元，請改成 ${變數}' >&2
  exit 1
fi
echo "shell 變數檢查 OK（$# 個檔案）"
