package bandlogin

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
)

//go:embed page.html
var pageHTML []byte

// page는 폰에서 여는 화면이다. 토큰은 주소의 # 뒤에 있어서 이 요청에는 실리지 않는다.
func (s *Server) page(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	_, _ = w.Write(pageHTML)
}

func decodeJSON(r *http.Request, v any, limit int64) error {
	return json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(v)
}
