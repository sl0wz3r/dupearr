package jellyfin

import (
	"context"
	"fmt"
)

// ActiveSessions reads GET /Sessions and returns every id a session names, playing or paused
// (research §3.5, S13): the item a client opened (NowPlayingItem.Id; the primary when an alternate
// plays, the part's own item while part 2 or later of a stack plays), its PrimaryVersionId, and
// the version actually playing (PlayState.MediaSourceId). Ids are normalized (lower-case, no
// dashes). The map is empty when nothing plays; an answer that cannot be read as a session list is
// an error, never "nothing is playing". Only an API key or an administrator sees every user's
// sessions (S14): Sections proves that.
func (c *Client) ActiveSessions(ctx context.Context) (map[string]bool, error) {
	var sessions []sessionDTO
	if err := c.getJSON(ctx, "/Sessions", nil, &sessions); err != nil {
		return nil, err
	}
	if sessions == nil {
		// "null" is not a session list: an empty list is "[]".
		return nil, fmt.Errorf("jellyfin: GET /Sessions: %w: the answer is not a session list", ErrIncomplete)
	}
	out := map[string]bool{}
	for _, s := range sessions {
		if s.NowPlayingItem != nil {
			if id := normID(s.NowPlayingItem.ID); id != "" {
				out[id] = true
			}
			if id := normID(s.NowPlayingItem.PrimaryVersionID); id != "" {
				out[id] = true
			}
		}
		if s.PlayState != nil {
			if id := normID(s.PlayState.MediaSourceID); id != "" {
				out[id] = true
			}
		}
	}
	return out, nil
}
