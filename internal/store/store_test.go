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
