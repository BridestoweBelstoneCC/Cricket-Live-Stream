package logobg

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	xdraw "golang.org/x/image/draw"
)

// Pixel-for-pixel comparison against the Python original (Pillow). Skipped unless the
// upstream clone and Python with Pillow are present.
func TestDifferentialAgainstPillow(t *testing.T) {
	upstream, _ := filepath.Abs("../../.upstream")
	if _, err := os.Stat(filepath.Join(upstream, "logo_bg.py")); err != nil {
		t.Skip("no upstream clone")
	}
	py, err := exec.LookPath("python")
	if err != nil || exec.Command(py, "-c", "import PIL").Run() != nil {
		t.Skip("no python with Pillow")
	}
	soft := canvas(120, 120, white)
	ellipse(soft, 20, 20, 100, 100, 0, gold)
	small := image.NewNRGBA(image.Rect(0, 0, 60, 60))
	xdraw.CatmullRom.Scale(small, small.Rect, soft, soft.Rect, xdraw.Src, nil)
	b := badge()
	b.SetNRGBA(150, 150, color.NRGBA{200, 10, 10, 90})
	dir := t.TempDir()
	for name, im := range map[string]*image.NRGBA{"ring": ring(200), "badge": b, "soft": small} {
		for _, mode := range Modes {
			in := filepath.Join(dir, name+".png")
			f, _ := os.Create(in)
			png.Encode(f, im)
			f.Close()
			want := filepath.Join(dir, name+"_"+mode+"_py.png")
			cmd := exec.Command(py, "-I", "-c", `import sys; sys.path.insert(0, sys.argv[1])
from PIL import Image
import logo_bg
out, _, why = logo_bg.remove_background(Image.open(sys.argv[2]), sys.argv[3])
assert why is None, why
out.save(sys.argv[4])`, upstream, in, mode, want)
			if msg, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s/%s: %v\n%s", name, mode, err, msg)
			}
			f, _ = os.Open(want)
			pyIm, err := png.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			p := ToNRGBA(pyIm)
			got, _, why := RemoveBackground(im, mode)
			if why != "" {
				t.Fatal(why)
			}
			diffs := 0
			for i := range got.Pix {
				d := int(got.Pix[i]) - int(p.Pix[i])
				if d < -1 || d > 1 {
					if diffs == 0 {
						px := i / 4
						t.Errorf("%s/%s: first mismatch at (%d,%d): go %v, pillow %v", name, mode,
							px%got.Rect.Dx(), px/got.Rect.Dx(), got.Pix[i/4*4:i/4*4+4], p.Pix[i/4*4:i/4*4+4])
					}
					diffs++
				}
			}
			if diffs > 0 {
				t.Errorf("%s/%s: %d channel values differ by more than 1", name, mode, diffs)
			}
		}
	}
}
