package local

import (
	"github.com/sheyiyuan/zero-beat-core/lyrics"
	"github.com/sheyiyuan/zero-beat-core/store"
	"testing"
)

func TestResourceLyricsMatchesDesktopPriorityAndTranslation(t *testing.T) {
	files := map[string]string{
		"track.lrc":           "corrupted",
		"lyrics/track.lrc":    "[00:01.00]original",
		"lyrics/track.zh.lrc": "[00:01.00]译文",
		"Artist - Title.lrc":  "[00:01.00]lower priority",
	}
	song := store.Song{Title: "Title", Artist: "Artist"}
	lines := ResourceLyrics("track.flac", song, files, lyrics.DefaultLocalConfig())
	if len(lines) != 1 || lines[0].Text != "original" || lines[0].Translation != "译文" {
		t.Fatalf("wrong fallback: %+v", lines)
	}
	delete(files, "lyrics/track.lrc")
	lines = ResourceLyrics("track.flac", song, files, lyrics.DefaultLocalConfig())
	if len(lines) != 1 || lines[0].Text != "lower priority" {
		t.Fatalf("metadata fallback: %+v", lines)
	}
	files = map[string]string{"unrelated.lrc": "[ti:Title]\n[ar:Artist]\n[00:02]tag match"}
	lines = ResourceLyrics("track.flac", song, files, lyrics.DefaultLocalConfig())
	if len(lines) != 1 || lines[0].Text != "tag match" {
		t.Fatalf("tags: %+v", lines)
	}
}
