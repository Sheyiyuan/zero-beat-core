package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
	"math"
	"regexp"
	"time"
)

type GUISettings struct {
	Theme       string            `json:"theme"`
	Palette     string            `json:"palette"`
	Accent      string            `json:"accent"`
	CloseAction string            `json:"close_action"`
	Shortcuts   map[string]string `json:"shortcuts"`
	Window      string            `json:"window"`
	Haptics     bool              `json:"haptics"`
}

func (f *Features) Preferences(ctx context.Context) (app.Preferences, error) {
	return feature(f, ctx, func(context.Context) (app.Preferences, error) { return f.s.svc.Preferences(), nil })
}
func (f *Features) GUISettings(ctx context.Context) (GUISettings, error) {
	return feature(f, ctx, func(context.Context) (GUISettings, error) {
		out := GUISettings{Theme: "system", Palette: "zero-red", CloseAction: "background", Shortcuts: map[string]string{}, Haptics: true}
		_ = json.Unmarshal([]byte(f.s.store.Setting("gui.settings", "")), &out)
		if out.Shortcuts == nil {
			out.Shortcuts = map[string]string{}
		}
		return out, nil
	})
}
func (f *Features) SaveGUISettings(ctx context.Context, in GUISettings) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		if in.Theme != "system" && in.Theme != "light" && in.Theme != "dark" {
			return false, fmt.Errorf("主题模式无效")
		}
		if in.Palette != "zero-red" && in.Palette != "hanpai-blue" {
			return false, fmt.Errorf("配色无效")
		}
		if in.CloseAction != "background" && in.CloseAction != "quit" && in.CloseAction != "mini" {
			return false, fmt.Errorf("关闭行为无效")
		}
		if in.Accent != "" && !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(in.Accent) {
			return false, fmt.Errorf("强调色无效")
		}
		if len(in.Window) > 8192 || (in.Window != "" && !json.Valid([]byte(in.Window))) {
			return false, fmt.Errorf("窗口设置无效")
		}
		allowed := map[string]bool{"toggle": true, "search": true, "previous": true, "next": true, "authors": true, "volume_down": true, "volume_up": true, "play_selected": true, "close_panel": true}
		for action, key := range in.Shortcuts {
			if !allowed[action] || len(key) > 64 {
				return false, fmt.Errorf("快捷键设置无效")
			}
		}
		if in.Shortcuts == nil {
			in.Shortcuts = map[string]string{}
		}
		raw, err := json.Marshal(in)
		if err == nil {
			err = f.s.store.SetSetting("gui.settings", string(raw))
		}
		if err == nil {
			f.changed("settings.changed")
		}
		return true, err
	})
	return err
}
func (f *Features) PlaybackSetting(ctx context.Context, key string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("数值无效")
	}
	_, err := feature(f, ctx, func(ctx context.Context) (bool, error) {
		err := f.s.call(ctx, func() error {
			s := f.s
			switch key {
			case "quality":
				if value < 0 || value > 2 || value != math.Trunc(value) {
					return fmt.Errorf("音质无效")
				}
				q := source.Quality(value)
				s.ctrl.SetQuality(q)
				s.svc.SaveQuality(q)
			case "mute":
				if s.ctrl.Muted() != (value != 0) {
					s.ctrl.ToggleMute()
				}
			case "comp":
				s.ctrl.SetCompDB(value)
				s.svc.SaveCompDB(s.ctrl.CompDB())
			case "song_comp":
				song, ok := s.ctrl.Current()
				if !ok {
					return ErrNotFound
				}
				s.ctrl.SetSongCompDB(value)
				s.svc.SaveSongCompDB(song.ID, s.ctrl.SongCompDB())
			case "auto_gain":
				if !s.ctrl.SetAutoGain(value != 0) {
					return fmt.Errorf("当前引擎不支持自动响度均衡")
				}
				s.svc.SaveAutoGain(value != 0)
			case "local_first":
				s.svc.SaveLocalFirst(value != 0)
			case "sleep":
				if value < 0 || value > 1440 {
					return fmt.Errorf("定时范围为 0–1440 分钟")
				}
				s.ctrl.SetSleep(time.Duration(value * float64(time.Minute)))
			default:
				return ErrUnsupported
			}
			s.publish()
			return nil
		})
		if err == nil {
			f.changed("settings.changed")
		}
		return true, err
	})
	return err
}
func (f *Features) Clip(ctx context.Context, id string) (store.Clip, error) {
	return feature(f, ctx, func(context.Context) (store.Clip, error) { return f.s.svc.Clip(id) })
}

// BeginClip pins an editor to this continuous visit to the current song.
// Returning to the same ID after another song does not revive the old editor.
func (f *Features) BeginClip(ctx context.Context, id string) (string, store.Clip, error) {
	var token string
	var clip store.Clip
	err := f.s.call(ctx, func() error {
		song, ok := f.s.ctrl.Current()
		if !ok || song.ID != id {
			return ErrStalePlayback
		}
		if f.s.clipTarget != id {
			f.s.clipTarget = id
			f.s.clipEpoch++
		}
		var err error
		clip, err = f.s.svc.Clip(id)
		token = fmt.Sprint(f.s.clipEpoch)
		return err
	})
	return token, clip, err
}

func (f *Features) EditClip(ctx context.Context, id, action string, clip store.Clip, at int64, token ...string) error {
	_, err := feature(f, ctx, func(ctx context.Context) (bool, error) {
		return true, f.s.call(ctx, func() error {
			s := f.s
			song, ok := s.ctrl.Current()
			if !ok || song.ID != id || (len(token) > 0 && token[0] != "" && token[0] != fmt.Sprint(s.clipEpoch)) {
				return ErrStalePlayback
			}
			duration := s.ctrl.Duration().Milliseconds()
			switch action {
			case "save", "clear":
				if action == "clear" {
					clip = store.Clip{}
				}
				if clip.StartMS < 0 || clip.EndMS < 0 || (clip.EndMS > 0 && clip.EndMS-clip.StartMS < 100) || (duration > 0 && clip.StartMS >= duration) {
					return fmt.Errorf("片段终点须晚于起点，且至少为 0.1 秒")
				}
				if duration > 0 && clip.EndMS >= duration {
					clip.EndMS = 0
				}
				if err := s.svc.SaveClip(id, clip); err != nil {
					return err
				}
				s.clipPreviewSong = ""
				s.clipPreviewClip = store.Clip{}
				s.startPlay(song, time.Duration(clip.StartMS)*time.Millisecond, 0, s.intent)
				f.changed("lyrics.changed")
			case "preview":
				s.clipPreviewSong = id
				s.clipPreviewClip = clip
				s.startPlay(song, time.Duration(max(0, at))*time.Millisecond, 0, s.intent)
			case "cancel":
				if s.clipPreviewSong == id {
					s.clipPreviewSong = ""
					s.clipPreviewClip = store.Clip{}
					clip, err := s.svc.Clip(id)
					if err != nil {
						return err
					}
					at := s.ctrl.Position().Milliseconds()
					if at < clip.StartMS || (clip.EndMS > 0 && at >= clip.EndMS) {
						at = clip.StartMS
					}
					s.startPlay(song, time.Duration(at)*time.Millisecond, 0, s.intent)
				}
			default:
				return ErrUnsupported
			}
			return nil
		})
	})
	return err
}
