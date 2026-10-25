package server

import "testing"

func TestIsPlayerUA(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"APTV/1.5 CFNetwork/1410.0.3 darwin/22.0.0", true}, // APTV iOS 实际形态
		{"aptv/2.0", true}, // 大小写不敏感
		{"Mozilla/5.0 (Macintosh) Chrome/130.0 Safari/537.36", false},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/604.1", false}, // 手机浏览器不误判
		{"okhttp/4.9.3", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isPlayerUA(c.ua); got != c.want {
			t.Errorf("isPlayerUA(%q) = %v, want %v", c.ua, got, c.want)
		}
	}
}
