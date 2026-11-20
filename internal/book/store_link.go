package book

import (
	"net/url"
	"strings"
)

var storeHosts = []string{
	"kyobobook.co.kr", "yes24.com", "aladin.co.kr", "interpark.com", "ypbooks.co.kr",
	"bandinlunis.com", "coupang.com", "ridibooks.com", "11st.co.kr", "gmarket.co.kr",
}

// IsStoreLink는 서점이나 쇼핑몰의 상품 링크인지 알려 준다. 구매 링크는 교보문고로 통일하므로 언급과 요약의 링크에서 뺀다.
func IsStoreLink(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	for _, h := range storeHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return strings.HasPrefix(host, "amazon.") || strings.Contains(host, ".amazon.") ||
		(host == "smartstore.naver.com" || host == "shopping.naver.com" || host == "search.shopping.naver.com")
}
