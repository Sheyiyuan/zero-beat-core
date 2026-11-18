package bilibili

import (
	"testing"
	"time"

	"github.com/sheyiyuan/zero-beat-core/bili"
	"github.com/sheyiyuan/zero-beat-core/source"
)

func TestMetadataAndQualityMapping(t *testing.T) {
	s := New(bili.New())
	if s.ID() != source.Bilibili || s.Name() != "Bilibili" {
		t.Fatalf("音源标识不符: %s %s", s.ID(), s.Name())
	}
	if !s.Caps().Has(source.CapMultiPart) || !s.Caps().Has(source.CapRemotePlaylists) || !s.Caps().Has(source.CapPlaylistWrite) {
		t.Fatal("B 站能力位不完整")
	}
	cases := map[source.Quality]bili.Quality{
		source.QualityAuto:     bili.QualityAuto,
		source.QualityHigh:     bili.QualityHigh,
		source.QualityLossless: bili.QualityLossless,
	}
	for in, want := range cases {
		if got := mapQuality(in); got != want {
			t.Fatalf("mapQuality(%v) = %v，期望 %v", in, got, want)
		}
	}
	if timeMS(1500) != 1500*time.Millisecond {
		t.Fatal("timeMS 换算错误")
	}
}

// 新能力是可选接口；原有 Source 契约不要求本地音源提供评论。
func TestContentCapabilitiesAndMapping(t *testing.T) {
	s := New(bili.New())
	if !s.Caps().Has(source.CapComments) || !s.Caps().Has(source.CapRelated) {
		t.Fatal("missing content capabilities")
	}
	created := time.Unix(1700000000, 0)
	got := convertComments([]bili.Comment{{ID: "1", Author: "读者", Avatar: "https://avatar", Content: "正文", Likes: 3, CreatedAt: created, ReplyCount: 4}})
	if len(got) != 1 || got[0] != (source.Comment{ID: "1", Author: "读者", Avatar: "https://avatar", Content: "正文", Likes: 3, CreatedAt: created, ReplyCount: 4}) {
		t.Fatalf("mapping: %+v", got)
	}
}
