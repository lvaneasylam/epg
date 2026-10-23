package store

import (
	"reflect"
	"testing"
)

func TestDedupChannels(t *testing.T) {
	all := []Channel{
		{ID: 3, Source: "B", Priority: 5, XmltvID: "b1", Name: "CCTV-1"},
		{ID: 1, Source: "A", Priority: 1, XmltvID: "a1", Name: "CCTV-1"}, // 最优：A 源优先级高
		{ID: 2, Source: "A", Priority: 1, XmltvID: "a2", Name: "CCTV-2"},
	}
	got := DedupChannels(all)
	want := []Channel{
		{ID: 1, Source: "A", Priority: 1, XmltvID: "a1", Name: "CCTV-1"},
		{ID: 2, Source: "A", Priority: 1, XmltvID: "a2", Name: "CCTV-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DedupChannels = %+v, want %+v", got, want)
	}

	// 同优先级取 id 较小者
	got2 := DedupChannels([]Channel{
		{ID: 9, Source: "B", Priority: 5, XmltvID: "b1", Name: "X"},
		{ID: 4, Source: "C", Priority: 5, XmltvID: "c1", Name: "X"},
	})
	if len(got2) != 1 || got2[0].ID != 4 {
		t.Errorf("同优先级应取 id 小者: %+v", got2)
	}
}

func TestFilterHidden(t *testing.T) {
	all := []Channel{
		{ID: 1, Name: "CCTV-1"},
		{ID: 2, Name: "CCTV-2"},
		{ID: 3, Name: "湖南卫视"},
	}

	// 空名单原样返回（含同切片引用，避免无谓拷贝）
	if got := FilterHidden(all, nil); len(got) != 3 {
		t.Errorf("空名单应全部保留: %+v", got)
	}

	got := FilterHidden(all, map[string]bool{"CCTV-2": true, "不存在": true})
	if len(got) != 2 || got[0].Name != "CCTV-1" || got[1].Name != "湖南卫视" {
		t.Errorf("FilterHidden = %+v, want CCTV-1/湖南卫视", got)
	}
}
