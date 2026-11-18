package local

import (
	"path"
	"sort"
	"strings"

	"github.com/sheyiyuan/zero-beat-core/library"
	"github.com/sheyiyuan/zero-beat-core/lyrics"
	"github.com/sheyiyuan/zero-beat-core/store"
)

// ResourceLyrics applies the desktop filename/tag ranking to a shallow SAF
// snapshot. Names are relative to the granted tree; no URI is treated as a path.
func ResourceLyrics(name string, song store.Song, files map[string]string, cfg lyrics.LocalConfig) []lyrics.Line {
	s := New(nil, cfg)
	stem, ext := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	var candidates []candidate
	for name := range files {
		dir := path.Dir(name)
		if (dir != "." && !strings.EqualFold(dir, "lyrics")) || !strings.HasSuffix(strings.ToLower(name), ".lrc") || isSidecar(name) {
			continue
		}
		rank := 0
		if dir != "." {
			rank = 1
		}
		candidates = append(candidates, candidate{path: name, name: path.Base(name), dirRank: rank, kind: s.matchKind(path.Base(name), stem, ext, song)})
	}
	// Make tag-budget selection deterministic even though input is a map.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })
	budget := 20
	for i := range candidates {
		c := &candidates[i]
		if c.kind != "" || budget == 0 || s.cfg.Rank(lyrics.MatchTag) > len(s.cfg.CandidateOrder) {
			continue
		}
		budget--
		title, artist := lyrics.ParseLocalLRCMeta(files[c.path])
		if matchKey(song.Title) != "" && matchKey(title) == matchKey(song.Title) && (matchKey(song.Artist) == "" || matchKey(artist) == "" || matchKey(artist) == matchKey(song.Artist)) {
			c.kind = lyrics.MatchTag
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if x, y := s.cfg.Rank(a.kind), s.cfg.Rank(b.kind); x != y {
			return x < y
		}
		if a.dirRank != b.dirRank {
			return a.dirRank < b.dirRank
		}
		return library.NaturalLess(a.name, b.name)
	})
	for _, c := range candidates {
		if c.kind == "" {
			continue
		}
		lines := s.parseText(files[c.path])
		if len(lines) == 0 {
			continue
		}
		if s.cfg.WantsSidecar() {
			stem := strings.TrimSuffix(c.path, path.Ext(c.path))
			for _, suffix := range []string{".trans.lrc", ".zh.lrc"} {
				names := []string{stem + suffix}
				if path.Dir(stem) == "." {
					names = append(names, "lyrics/"+stem+suffix)
				}
				found := false
				for _, wanted := range names {
					for actual, text := range files {
						if strings.EqualFold(actual, wanted) {
							translated := lyrics.ParseLocalLRC(text)
							if len(translated) > 0 {
								lines = lyrics.MergeSidecar(lines, translated)
								found = true
							}
							break
						}
					}
					if found {
						break
					}
				}
				if found {
					break
				}
			}
		}
		return lines
	}
	return nil
}
