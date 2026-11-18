//go:build !android

package download

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/source"
	"os"
	"os/exec"
	"strings"
)

func platformRemux(ctx context.Context, in, out string, track source.Track) error {
	if err := ffmpegRemux(ctx, in, out, track); err != nil {
		return err
	}
	data, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_format", "-show_streams", "-of", "json", out).Output()
	if err != nil {
		return fmt.Errorf("校验封装文件失败: %w", err)
	}
	var result struct {
		Streams []struct {
			CodecType string            `json:"codec_type"`
			CodecName string            `json:"codec_name"`
			Tags      map[string]string `json:"tags"`
		}
		Format struct {
			Tags map[string]string `json:"tags"`
		}
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return err
	}
	tags := map[string]string{}
	for k, v := range result.Format.Tags {
		tags[strings.ToLower(k)] = v
	}
	audio := false
	for _, s := range result.Streams {
		if s.CodecType == "audio" && s.CodecName != "" {
			audio = true
			for k, v := range s.Tags {
				tags[strings.ToLower(k)] = v
			}
		}
	}
	if !audio {
		return fmt.Errorf("封装文件没有可识别的音频")
	}
	for k, v := range map[string]string{"title": track.Title, "artist": track.Artist, "album": track.Album} {
		if strings.TrimSpace(v) != "" && tags[k] != strings.TrimSpace(v) {
			return fmt.Errorf("封装标签 %s 校验失败", k)
		}
	}
	return nil
}

func ffmpegRemux(ctx context.Context, in, out string, track source.Track) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("找不到 ffmpeg，无法封装音频: %w", err)
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", in}
	for _, kv := range [][2]string{
		{"title", track.Title},
		{"artist", track.Artist},
		{"album", track.Album},
	} {
		if v := strings.TrimSpace(kv[1]); v != "" {
			args = append(args, "-metadata", kv[0]+"="+v)
		}
	}
	args = append(args, "-vn", "-c", "copy", out)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(out)
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("封装音频失败: %w: %s", err, msg)
		}
		return fmt.Errorf("封装音频失败: %w", err)
	}
	return nil
}
