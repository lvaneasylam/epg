package syncer

import "testing"

func TestIsJunkTitle(t *testing.T) {
	junk := []string{
		"由https://epg.136605.xyz提供节目单服务",
		"由epg.51zmt.top:8000提供节目单服务",
		"暂无节目单1",
		"精彩节目-暂未提供节目预告信息",
		"頻道尚未提供當日節目表",
		"此頻道未能提供節目表敬請原諒!",
	}
	for _, s := range junk {
		if !isJunkTitle(s) {
			t.Errorf("应过滤: %q", s)
		}
	}
	real := []string{
		"提供演唱會及MV點播服務",
		"24小時不停提供投資市場最新報價",
		"香港都會大學提供:知識新天地[粵]",
		"鶴瓶のええ歌やなぁ　マイク眞木「バラが咲いた」&坂本九さんへの楽曲提供秘話",
		"朝闻天下",
	}
	for _, s := range real {
		if isJunkTitle(s) {
			t.Errorf("误杀真实节目: %q", s)
		}
	}
}
