package httpapi

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImageEditMaskCompositeUsesAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	src.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 255})
	mask := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	mask.SetNRGBA(0, 0, color.NRGBA{A: 0})
	mask.SetNRGBA(1, 0, color.NRGBA{A: 255})

	body := fmt.Sprintf(
		`{"prompt":"edit","images":[{"image_url":%q}],"mask":{"image_url":%q}}`,
		dataPNG(t, src),
		dataPNG(t, mask),
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	_, _, _, _, _, images, masks, err := parseImageEditRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	composited, err := compositeEditMasks(images, masks)
	if err != nil {
		t.Fatal(err)
	}
	if len(composited) != 1 || composited[0].MimeType != "image/png" {
		t.Fatalf("composited = %#v", composited)
	}
	got, _, err := image.Decode(bytes.NewReader(composited[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, leftAlpha := got.At(0, 0).RGBA()
	_, _, _, rightAlpha := got.At(1, 0).RGBA()
	if leftAlpha != 0 || rightAlpha != 0xffff {
		t.Fatalf("alpha left=%x right=%x", leftAlpha, rightAlpha)
	}
}

func dataPNG(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}
