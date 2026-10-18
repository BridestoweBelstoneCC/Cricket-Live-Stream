package scorerfile

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

var tricky = [][]byte{
	[]byte("plain ascii"),
	[]byte("caf\xc3\xa9 \xe2\x80\x94 dash"),
	[]byte("\xef\xbb\xbfBOM first"),
	[]byte("bad \xff byte"),
	[]byte("truncated \xe2\x82 euro"),
	[]byte("truncated at end \xe2\x82"),
	[]byte("lone continuation \x80\x80 x"),
	[]byte("overlong \xc0\xaf slash"),
	[]byte("surrogate \xed\xa0\x80 half"),
	[]byte("4-byte \xf0\x9f\x8f\x8f bat, cut \xf0\x9f\x8f"),
	[]byte("crlf\r\nand cr\rand lf\n"),
	[]byte("latin-1 Smith \xe9 Jones"),
}

func TestDecodeTextIsPythonsUTF8Replace(t *testing.T) {
	var py string
	for _, c := range []string{"python3", "python", "py"} {
		if p, err := exec.LookPath(c); err == nil && exec.Command(p, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no Python 3")
	}
	var hexes []string
	for _, b := range tricky {
		hexes = append(hexes, strings.ToUpper(string(hexOf(b))))
	}
	in, _ := json.Marshal(hexes)
	cmd := exec.Command(py, "-I", "-c", `
import io, json, sys
out = []
for h in json.loads(sys.stdin.read()):
    b = bytes.fromhex(h)
    out.append(io.TextIOWrapper(io.BytesIO(b), encoding="utf-8", errors="replace").read())
sys.stdout.buffer.write(json.dumps(out).encode())
`)
	cmd.Stdin = strings.NewReader(string(in))
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	for i, in := range tricky {
		if got := DecodeText(in); got != want[i] {
			t.Errorf("%q:\n  Go     %q\n  Python %q", in, got, want[i])
		}
	}
}

func hexOf(b []byte) []byte {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return out
}
