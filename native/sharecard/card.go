package sharecard

import (
	_ "embed"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

//go:embed fonts/noto-sans-bold.ttf
var boldFont []byte

//go:embed fonts/noto-sans-regular.ttf
var regularFont []byte

const (
	Width       = 1200
	Height      = 630
	coverX      = 48
	coverY      = 75
	coverWidth  = 640
	coverHeight = 480
	coverRadius = 12
	textX       = 736
	textWidth   = 416
	titleSize   = 52
	titleLine   = 64
	titleLines  = 3
	ownerSize   = 28
	playSize    = 26
	maxCover    = 4096
)

var (
	background  = color.RGBA{0x10, 0x10, 0x12, 0xff}
	placeholder = color.RGBA{0x1c, 0x1c, 0x20, 0xff}
	frame       = color.RGBA{0x2a, 0x2a, 0x2e, 0xff}
	white       = color.RGBA{0xff, 0xff, 0xff, 0xff}
	grey        = color.RGBA{0xa1, 0xa1, 0xaa, 0xff}
	purple      = color.RGBA{0xa7, 0x8b, 0xfa, 0xff}

	errUnsupportedText = errors.New("the title or owner uses letters the card font does not have")
	errEmptyText       = errors.New("the title or owner is empty")
)

type faces struct {
	title font.Face
	owner font.Face
	play  font.Face
}

var (
	loadedFaces faces
	facesErr    error
	facesOnce   sync.Once
)

func cardFaces() (faces, error) {
	facesOnce.Do(func() {
		loadedFaces, facesErr = loadFaces()
	})
	return loadedFaces, facesErr
}

func loadFaces() (faces, error) {
	bold, err := opentype.Parse(boldFont)
	if err != nil {
		return faces{}, err
	}
	regular, err := opentype.Parse(regularFont)
	if err != nil {
		return faces{}, err
	}
	face := func(f *opentype.Font, size float64) (font.Face, error) {
		return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	}
	title, err := face(bold, titleSize)
	if err != nil {
		return faces{}, err
	}
	owner, err := face(regular, ownerSize)
	if err != nil {
		return faces{}, err
	}
	play, err := face(bold, playSize)
	if err != nil {
		return faces{}, err
	}
	return faces{title: title, owner: owner, play: play}, nil
}

func hasGlyph(face font.Face, r rune) bool {
	_, ok := face.GlyphAdvance(r)
	return ok
}

func CardText(face font.Face, value string) (string, error) {
	var kept strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsSpace(r):
			kept.WriteRune(' ')
		case hasGlyph(face, r):
			kept.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			return "", errUnsupportedText
		}
	}
	text := strings.Join(strings.Fields(kept.String()), " ")
	if text == "" {
		return "", errEmptyText
	}
	return text, nil
}

func measure(face font.Face, value string) int {
	return font.MeasureString(face, value).Ceil()
}

func clip(face font.Face, value string, width int) string {
	if measure(face, value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && measure(face, string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return strings.TrimRight(string(runes), " ") + "…"
}

func WrapTitle(face font.Face, title string, width, maxLines int) []string {
	words := strings.Fields(title)
	lines := []string{}
	for len(words) > 0 && len(lines) < maxLines-1 {
		count := 1
		for count < len(words) && measure(face, strings.Join(words[:count+1], " ")) <= width {
			count++
		}
		lines = append(lines, clip(face, strings.Join(words[:count], " "), width))
		words = words[count:]
	}
	if len(words) > 0 {
		lines = append(lines, clip(face, strings.Join(words, " "), width))
	}
	return lines
}

type roundedRect struct {
	bounds image.Rectangle
	radius float64
}

func (r roundedRect) ColorModel() color.Model { return color.AlphaModel }

func (r roundedRect) Bounds() image.Rectangle { return r.bounds }

func (r roundedRect) At(x, y int) color.Color {
	px, py := float64(x)+0.5, float64(y)+0.5
	minX, minY := float64(r.bounds.Min.X), float64(r.bounds.Min.Y)
	maxX, maxY := float64(r.bounds.Max.X), float64(r.bounds.Max.Y)
	cx := min(max(px, minX+r.radius), maxX-r.radius)
	cy := min(max(py, minY+r.radius), maxY-r.radius)
	dx, dy := px-cx, py-cy
	distance := dx*dx + dy*dy
	if distance <= (r.radius-0.5)*(r.radius-0.5) {
		return color.Alpha{0xff}
	}
	if distance >= (r.radius+0.5)*(r.radius+0.5) {
		return color.Alpha{0}
	}
	return color.Alpha{0x80}
}

func fillRounded(dst draw.Image, bounds image.Rectangle, radius float64, fill color.Color) {
	draw.DrawMask(dst, bounds, image.NewUniform(fill), image.Point{}, roundedRect{bounds, radius}, bounds.Min, draw.Over)
}

func coverCrop(src image.Rectangle) image.Rectangle {
	w, h := src.Dx(), src.Dy()
	if w*coverHeight > h*coverWidth {
		cropped := h * coverWidth / coverHeight
		x := src.Min.X + (w-cropped)/2
		return image.Rect(x, src.Min.Y, x+cropped, src.Max.Y)
	}
	cropped := w * coverHeight / coverWidth
	y := src.Min.Y + (h-cropped)/2
	return image.Rect(src.Min.X, y, src.Max.X, y+cropped)
}

func loadCover(path string) (image.Image, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxCover || config.Height > maxCover {
		return nil, errors.New("the cover is too large")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	cover, _, err := image.Decode(file)
	return cover, err
}

func drawText(dst draw.Image, face font.Face, fill color.Color, x, y int, value string) {
	drawer := font.Drawer{Dst: dst, Src: image.NewUniform(fill), Face: face, Dot: fixed.P(x, y)}
	drawer.DrawString(value)
}

func Draw(cover image.Image, title, owner string) (*image.RGBA, error) {
	faces, err := cardFaces()
	if err != nil {
		return nil, err
	}
	titleText, err := CardText(faces.title, title)
	if err != nil {
		return nil, err
	}
	ownerText, err := CardText(faces.owner, owner)
	if err != nil {
		return nil, err
	}

	card := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(card, card.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)

	coverBounds := image.Rect(coverX, coverY, coverX+coverWidth, coverY+coverHeight)
	fillRounded(card, coverBounds.Inset(-2), coverRadius+2, frame)
	if cover == nil {
		fillRounded(card, coverBounds, coverRadius, placeholder)
	} else {
		scaled := image.NewRGBA(coverBounds)
		xdraw.CatmullRom.Scale(scaled, coverBounds, cover, coverCrop(cover.Bounds()), draw.Src, nil)
		draw.DrawMask(card, coverBounds, scaled, coverBounds.Min, roundedRect{coverBounds, coverRadius}, coverBounds.Min, draw.Over)
	}

	lines := WrapTitle(faces.title, titleText, textWidth, titleLines)
	titleTop := coverY + titleSize
	for index, line := range lines {
		drawText(card, faces.title, white, textX, titleTop+index*titleLine, line)
	}
	ownerY := titleTop + (len(lines)-1)*titleLine + 56
	drawText(card, faces.owner, grey, textX, ownerY, clip(faces.owner, "@"+ownerText, textWidth))
	drawText(card, faces.play, purple, textX, coverY+coverHeight-8, "Play on MistWarp")
	return card, nil
}

func write(card image.Image, outPath string) error {
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".card-*.png")
	if err != nil {
		return err
	}
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(temp, card); err != nil {
		temp.Close()
		os.Remove(temp.Name())
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(temp.Name())
		return err
	}
	return os.Rename(temp.Name(), outPath)
}

func removeOlder(outPath string) {
	base := filepath.Base(outPath)
	dash := strings.LastIndex(base, "-")
	if dash <= 0 {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(outPath), base[:dash+1]+"*.png"))
	for _, match := range matches {
		if filepath.Base(match) != base {
			os.Remove(match)
		}
	}
}

func Render(coverPath, title, owner, outPath string) string {
	cover, err := loadCover(coverPath)
	if err != nil {
		cover = nil
	}
	card, err := Draw(cover, title, owner)
	if err != nil {
		return err.Error()
	}
	if err := write(card, outPath); err != nil {
		return err.Error()
	}
	removeOlder(outPath)
	return ""
}
