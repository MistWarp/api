package sharecard

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCover(t *testing.T, dir string, width, height int) string {
	t.Helper()
	cover := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cover.Set(x, y, color.RGBA{uint8(x), uint8(y), 0x80, 0xff})
		}
	}
	path := filepath.Join(dir, "cover.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, cover); err != nil {
		t.Fatal(err)
	}
	return path
}

func readPNG(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoded, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestRenderWritesACardWithTheCover(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "cards", "p55-100.png")
	if reason := Render(writeCover(t, dir, 480, 360), "Meowing Simulator", "flufi", out); reason != "" {
		t.Fatal(reason)
	}
	card := readPNG(t, out)
	if card.Bounds() != image.Rect(0, 0, Width, Height) {
		t.Fatalf("card is %v", card.Bounds())
	}
	if r, _, b, _ := card.At(coverX+coverWidth/2, coverY+coverHeight/2).RGBA(); r>>8 < 0x60 || b>>8 < 0x70 {
		t.Fatal("the cover is not drawn in its frame")
	}
	if r, g, b, _ := card.At(coverX+1, coverY+1).RGBA(); r>>8 > 0x40 || g>>8 > 0x40 || b>>8 > 0x40 {
		t.Fatal("the cover corner is not rounded")
	}
}

func TestRenderWithoutACoverDrawsAPlaceholder(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p55-1.png")
	if reason := Render("", "Cat Game", "alice", out); reason != "" {
		t.Fatal(reason)
	}
	r, g, b, _ := readPNG(t, out).At(coverX+coverWidth/2, coverY+coverHeight/2).RGBA()
	if (color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xff}) != placeholder {
		t.Fatal("expected the placeholder colour")
	}
}

func TestABrokenCoverStillGetsACard(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "cover.png")
	os.WriteFile(broken, []byte("not an image"), 0o644)
	if reason := Render(broken, "Cat Game", "alice", filepath.Join(dir, "p55-1.png")); reason != "" {
		t.Fatal(reason)
	}
}

func TestRenderRefusesTitlesInScriptsTheFontLacks(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p55-1.png")
	if reason := Render("", "猫のゲーム", "alice", out); reason == "" {
		t.Fatal("expected a refusal")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("no card should be written")
	}
}

func TestRenderReplacesOlderVersionsOfTheSameProject(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"p55-1.png", "p55-2.png", "p56-1.png"} {
		os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o644)
	}
	if reason := Render("", "Cat Game", "alice", filepath.Join(dir, "p55-3.png")); reason != "" {
		t.Fatal(reason)
	}
	entries, _ := os.ReadDir(dir)
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != "p55-3.png,p56-1.png" {
		t.Fatalf("left %v", names)
	}
}

func TestCardText(t *testing.T) {
	faces, err := cardFaces()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"Cat  game 🐱!":   "Cat game !",
		"Ёлка – Ελλάδα":  "Ёлка – Ελλάδα",
		"  spaced\nout ": "spaced out",
	}
	for input, want := range cases {
		got, err := CardText(faces.title, input)
		if err != nil || got != want {
			t.Errorf("CardText(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"Космос 宇宙", "🐱", ""} {
		if _, err := CardText(faces.title, input); err == nil {
			t.Errorf("CardText(%q) should be refused", input)
		}
	}
}

func TestWrapTitle(t *testing.T) {
	faces, err := cardFaces()
	if err != nil {
		t.Fatal(err)
	}
	if got := WrapTitle(faces.title, "Cat Game", textWidth, titleLines); strings.Join(got, "|") != "Cat Game" {
		t.Fatalf("short title wrapped to %v", got)
	}
	long := WrapTitle(faces.title, "The Extremely Long Adventures of a Very Small Cat Who Wanted Everything", textWidth, titleLines)
	if len(long) != titleLines || !strings.HasSuffix(long[titleLines-1], "…") {
		t.Fatalf("long title wrapped to %v", long)
	}
	for _, line := range append(long, WrapTitle(faces.title, "Supercalifragilisticexpialidocious", textWidth, titleLines)...) {
		if measure(faces.title, line) > textWidth {
			t.Fatalf("%q is wider than the column", line)
		}
	}
}

func TestCoverCropKeepsTheMiddleAtFourByThree(t *testing.T) {
	cases := map[image.Rectangle]image.Rectangle{
		image.Rect(0, 0, 450, 250): image.Rect(58, 0, 391, 250),
		image.Rect(0, 0, 480, 360): image.Rect(0, 0, 480, 360),
		image.Rect(0, 0, 300, 600): image.Rect(0, 187, 300, 412),
	}
	for src, want := range cases {
		if got := coverCrop(src); got != want {
			t.Errorf("coverCrop(%v) = %v, want %v", src, got, want)
		}
	}
}
