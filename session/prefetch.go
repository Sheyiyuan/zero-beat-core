package session

import (
	"context"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/media"
	"github.com/sheyiyuan/zero-beat-core/player"
	"github.com/sheyiyuan/zero-beat-core/store"
	"time"
)

// Prefetch follows the controller's rules: do not guess shuffle or loop-one.
func (s *Session) prefetchNext() {
	state := s.ctrl.Export()
	index := player.NextPlayableIndex(state.Mode, state.Index, len(state.Queue))
	if index < 0 || state.Awaiting || state.PlaybackState != media.StatePlaying || s.autoHold.Load() {
		if s.prefetchCancel != nil {
			s.prefetchCancel()
			s.prefetchCancel = nil
		}
		s.prefetchKey = ""
		return
	}
	song := store.NormalizeSong(state.Queue[index])
	key := fmt.Sprintf("%s:%s:%d:%t", s.playbackID, song.ID, state.Quality, s.svc.LocalFirstEnabled())
	if key == s.prefetchKey {
		return
	}
	if s.prefetchCancel != nil {
		s.prefetchCancel()
	}
	s.prefetchKey = key
	s.prefetch.Clear()
	localFirst := s.svc.LocalFirstEnabled()
	ctx, cancel := context.WithTimeout(s.root, 20*time.Second)
	s.prefetchCancel = cancel
	s.resolveWG.Add(1)
	go func() {
		defer s.resolveWG.Done()
		defer cancel()
		resolved, stream, err := s.svc.Resolve(ctx, song, state.Quality)
		s.post(func() {
			if err == nil && s.prefetchKey == key && s.ctrl.NextSongID() == song.ID {
				s.prefetch.Set(song.ID, stream)
				s.prefetchSong = resolved
				s.prefetchQuality = state.Quality
				s.prefetchLocal = localFirst
			}
		})
	}()
}
