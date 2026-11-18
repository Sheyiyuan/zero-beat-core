//go:build !android

package download

import (
	"bytes"
	"context"
	"github.com/sheyiyuan/zero-beat-core/source"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemuxPreservesAudioAndWritesTags(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	for _, format := range []struct{ ext, codec string }{{"m4a", "aac"}, {"flac", "flac"}, {"mp3", "libmp3lame"}, {"opus", "libopus"}} {
		t.Run(format.ext, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "input."+format.ext)
			out := filepath.Join(dir, "output."+format.ext)
			if data, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.12", "-c:a", format.codec, in).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, data)
			}
			track := source.Track{Title: "零拍测试歌曲", Artist: "作者甲 / 作者乙", Album: "本地回归"}
			if err := platformRemux(context.Background(), in, out, track); err != nil {
				t.Fatal(err)
			}
			hash := func(path string) []byte {
				raw, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-c", "copy", "-f", "hash", "-hash", "sha256", "-").Output()
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			if !bytes.Equal(hash(in), hash(out)) {
				t.Fatal("remux changed compressed audio packets")
			}
		})
	}
}
func TestRemuxRejectsCorruptInput(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "broken.part")
	if err := os.WriteFile(in, []byte("not an audio file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := platformRemux(context.Background(), in, filepath.Join(dir, "out.m4a"), source.Track{Title: "broken"}); err == nil {
		t.Fatal("corrupt audio accepted")
	}
}
