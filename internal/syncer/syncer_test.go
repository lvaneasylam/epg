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

func TestNormalizeChName(t *testing.T) {
	// 分辨率标记词与空白差异应归一为同一键
	//（连字符等其余差异不在归一范围：CCTV-1 与 CCTV1 是不同键）
	groups := [][]string{
		{"CCTV1", "CCTV1 高清", "CCTV1高清", "cctv1 hd"},
		{"CCTV-1", "CCTV-1-HD", "CCTV-1 高清"},
		{"湖南卫视", "湖南卫视 标清", "湖南卫视標清"},
		{"tvN", "tvN HD", "TVN FHD"},
		{"浙江卫视", "浙江卫视超清", "浙江卫视 4K", "浙江卫视 8K"},
	}
	for _, g := range groups {
		first := normalizeChName(g[0])
		for _, s := range g[1:] {
			if got := normalizeChName(s); got != first {
				t.Errorf("%q 归一为 %q，应与 %q 一致", s, got, first)
			}
		}
	}

	// 不同频道不得归一为同一键（含 HD 位于单词内部的误伤防护）
	distinct := []string{
		"CCTV1", "CCTV2",
		"CHILDHOOD CHANNEL", // HD 在 CHILDHOOD 内部，不应被删
		"东方卫视", "江苏卫视",
	}
	seen := map[string]string{}
	for _, s := range distinct {
		key := normalizeChName(s)
		if prev, ok := seen[key]; ok {
			t.Errorf("%q 与 %q 归一冲突（键 %q）", s, prev, key)
		}
		seen[key] = s
	}
	// 空白删除属比较键设计（"cctv 1" 与 "cctv1" 同键），但 HD 不得被删
	if got := normalizeChName("CHILDHOOD CHANNEL"); got != "childhoodchannel" {
		t.Errorf("单词内部 HD 被误删: %q", got)
	}
}
