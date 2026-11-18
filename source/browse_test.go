package source

import "testing"

func TestParseBrowseTarget(t *testing.T) {
	cases := map[string]BrowseRef{
		"netease:artist:6452":                     {NetEase, BrowseArtist, "6452"},
		"https://music.163.com/#/user/home?id=12": {NetEase, BrowseUser, "12"},
		"https://music.163.com/djradio?id=8":      {NetEase, BrowseRadio, "8"},
		"https://space.bilibili.com/122541/video": {Bilibili, BrowseUser, "122541"},
	}
	for raw, want := range cases {
		got, err := ParseBrowseTarget(raw)
		if err != nil || got != want {
			t.Fatalf("%s: %+v %v", raw, got, err)
		}
	}
	for _, raw := range []string{"6452", "netease:user:-1", "bilibili:artist:1", "https://music.163.com.evil/artist?id=1", "https://music.163.com/song?id=1"} {
		if _, err := ParseBrowseTarget(raw); err == nil {
			t.Fatal(raw)
		}
	}
}
