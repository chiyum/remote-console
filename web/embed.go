// Package web 內嵌總覽頁靜態檔，讓整個服務是單一 binary（搬機只要 go build）。
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static 回傳以 static/ 為根的檔案系統。
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
