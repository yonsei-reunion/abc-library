package book

import (
	"net/url"
	"testing"
)

func TestIsStoreLink(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://product.kyobobook.co.kr/detail/S000001981838": true,
		"https://www.yes24.com/Product/Goods/1":                true,
		"https://aladin.co.kr/shop/wproduct.aspx?ItemId=1":     true,
		"https://www.amazon.com/dp/1":                          true,
		"https://www.amazon.co.jp/dp/1":                        true,
		"https://ridibooks.com/books/1":                        true,
		"https://smartstore.naver.com/shop/1":                  true,
		"https://www.hani.co.kr/arti/1.html":                   false,
		"https://www.youtube.com/watch?v=1":                    false,
		"https://blog.naver.com/x/1":                           false,
		"https://notkyobobook.co.kr/":                          false,
	} {
		u, _ := url.Parse(raw)
		if got := IsStoreLink(u); got != want {
			t.Errorf("IsStoreLink(%s) = %v, want %v", raw, got, want)
		}
	}
}
