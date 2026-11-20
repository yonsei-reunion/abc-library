package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

// pwaFiles는 사이트 루트에서 내보내야 하는 PWA 파일이다. 서비스 워커는 자기 경로 아래만 다루므로 /static/에 둘 수 없다.
//
//go:embed pwa
var pwaFiles embed.FS

// precached는 서비스 워커가 설치될 때 받아 두는 정적 파일이다. 오프라인 화면을 그리는 데 쓴다.
var precached = []string{"style.css", "htmx.min.js", "app.js", "icon.svg"}

// serviceWorker는 정적 파일의 해시를 VERSION으로, 미리 받을 주소를 PRECACHE로 채운 서비스 워커다.
var serviceWorker = sync.OnceValue(func() []byte {
	sum := sha256.New()
	_ = fs.WalkDir(staticFiles, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := staticFiles.ReadFile(path)
		if err != nil {
			return err
		}
		sum.Write([]byte(path))
		sum.Write(data)
		return nil
	})
	src, _ := pwaFiles.ReadFile("pwa/sw.js")
	sum.Write(src)
	urls := []string{"/offline"}
	for _, name := range precached {
		urls = append(urls, staticURL(name))
	}
	list, _ := json.Marshal(urls)
	return []byte(strings.NewReplacer(
		"'__VERSION__'", "'"+hex.EncodeToString(sum.Sum(nil)[:4])+"'",
		"__PRECACHE__", string(list),
	).Replace(string(src)))
})

func (s *Server) pwaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, _ *http.Request) {
		data, _ := pwaFiles.ReadFile("pwa/manifest.webmanifest")
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		// 새 버전을 바로 알아채도록 브라우저와 Firebase Hosting이 캐시하지 않게 한다.
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(serviceWorker())
	})
	// 서비스 워커가 미리 받아 두었다가 연결이 없을 때 보여 주는 화면이다.
	mux.HandleFunc("GET /offline", func(w http.ResponseWriter, r *http.Request) {
		s.status(w, r, http.StatusOK, "인터넷에 연결되지 않았습니다", "연결되면 다시 시도해 주세요. 한 번 열어 본 화면은 연결 없이도 볼 수 있습니다.")
	})
}
