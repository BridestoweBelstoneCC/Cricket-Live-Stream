package obs

import (
	"sort"
	"strings"

	"cricketstream/internal/pyval"
)

// ── Scene stacking ───────────────────────────────────────────────────────────

// PictureKinds put a whole picture on screen: cameras, video files, screen capture. The
// graphics (Overlay, ReplayClip) must sit ABOVE these or they're hidden. Anything else
// above the graphics — a sponsor logo image, say — is the club's deliberate layout and is
// left exactly where it is. An earlier version lifted the graphics to the very top of
// the scene and moved a club's two sponsor logos underneath its scorebar.
var PictureKinds = []string{"ffmpeg_source", "vlc_source", "dshow_input", "v4l2_input",
	"decklink-input", "ndi_source", "monitor_capture", "window_capture",
	"game_capture", "screen_capture", "display_capture", "xshm_input",
	"pipewire-screen-capture-source"}

func IsPictureKind(kind string) bool {
	kind = strings.ToLower(kind)
	for _, k := range PictureKinds {
		if kind == k {
			return true
		}
	}
	for _, p := range []string{"av_capture", "macos-avcapture", "coreaudio_video", "syphon"} {
		if strings.HasPrefix(kind, p) {
			return true
		}
	}
	return false
}

// FillCanvasTransform scales to fit the canvas (letterboxed if the aspect differs),
// anchored top-left.
func FillCanvasTransform(w, h int) map[string]any {
	return map[string]any{"boundsType": "OBS_BOUNDS_SCALE_INNER", "boundsWidth": w,
		"boundsHeight": h, "boundsAlignment": 0, "alignment": 5,
		"positionX": 0, "positionY": 0}
}

// UntouchedPlacement: still where OBS dropped it — native size, top-left, no bounds.
// Anything else is someone's deliberate layout (a picture-in-picture, say) and isn't
// ours to undo.
func UntouchedPlacement(tr map[string]any) bool {
	bt, has := tr["boundsType"]
	if has && bt != nil && bt != "OBS_BOUNDS_NONE" {
		return false
	}
	scale := func(k string) float64 {
		if v, ok := tr[k]; ok {
			f, _ := pyval.Float(v)
			return f
		}
		return 1
	}
	return !pyval.Truthy(tr["positionX"]) && !pyval.Truthy(tr["positionY"]) &&
		scale("scaleX") == 1 && scale("scaleY") == 1
}

func itemIndex(it map[string]any) int { return pyval.IntOr(it["sceneItemIndex"], 0) }

// CoveringPictures lists the picture sources stacked ABOVE source (a graphic), LOWEST
// first. The fix is to move each of those DOWN to just beneath the graphic, not to lift
// the graphic up: lifting it past a camera that's been dragged over everything would
// also lift it past the logos in between, which the club put above the scorebar on
// purpose. items are GetSceneItemList's sceneItems; index 0 is the bottom.
func CoveringPictures(items []map[string]any, source string) []map[string]any {
	var me map[string]any
	for _, it := range items {
		if it["sourceName"] == source {
			me = it
			break
		}
	}
	if me == nil {
		return nil
	}
	mine := itemIndex(me)
	var above []map[string]any
	for _, it := range items {
		k, _ := it["inputKind"].(string)
		if it["sourceName"] != source && IsPictureKind(k) && itemIndex(it) > mine {
			above = append(above, it)
		}
	}
	// Lowest first: each lands directly beneath the graphic in turn, so two cameras keep
	// their order relative to each other (which one viewers see must not swap).
	sort.SliceStable(above, func(i, j int) bool { return itemIndex(above[i]) < itemIndex(above[j]) })
	return above
}
