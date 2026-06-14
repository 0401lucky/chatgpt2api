package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"chatgpt2api-go-backend/internal/protocol"
)

type editableImage struct {
	Data     []byte
	FileName string
	MimeType string
}

func parseImageEditRequest(r *http.Request) (prompt, model, size, responseFormat string, stream bool, images []editableImage, masks []editableImage, err error) {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "application/json") {
		var body map[string]any
		reader := io.LimitReader(r.Body, 32<<20)
		if decodeErr := json.NewDecoder(reader).Decode(&body); decodeErr != nil {
			return "", "", "", "", false, nil, nil, fmt.Errorf("invalid json body")
		}
		prompt = strings.TrimSpace(fmt.Sprint(body["prompt"]))
		if prompt == "" {
			return "", "", "", "", false, nil, nil, fmt.Errorf("prompt is required")
		}
		model = defaultString(body["model"], "gpt-image-2")
		size = strings.TrimSpace(fmt.Sprint(body["size"]))
		responseFormat = defaultString(body["response_format"], "b64_json")
		stream = parseBool(body["stream"])
		images, err = parseJSONEditImages(body["images"], "images", true)
		if err != nil {
			return
		}
		masks, err = parseJSONEditImages(body["mask"], "mask", false)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return "", "", "", "", false, nil, nil, err
	}
	prompt = strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		return "", "", "", "", false, nil, nil, fmt.Errorf("prompt is required")
	}
	model = defaultString(r.FormValue("model"), "gpt-image-2")
	size = strings.TrimSpace(r.FormValue("size"))
	responseFormat = defaultString(r.FormValue("response_format"), "b64_json")
	stream = parseBool(r.FormValue("stream"))
	images, err = parseMultipartEditImages(r.MultipartForm, []string{"image", "image[]"}, true)
	if err != nil {
		return
	}
	masks, err = parseMultipartEditImages(r.MultipartForm, []string{"mask", "mask[]"}, false)
	return
}

func parseJSONEditImages(raw any, field string, required bool) ([]editableImage, error) {
	if raw == nil {
		if required {
			return nil, fmt.Errorf("%s is required", field)
		}
		return nil, nil
	}
	var items []any
	switch value := raw.(type) {
	case []any:
		items = value
	default:
		items = []any{value}
	}
	if len(items) == 0 {
		if required {
			return nil, fmt.Errorf("%s must be a non-empty array", field)
		}
		return nil, nil
	}
	images := make([]editableImage, 0, len(items))
	for index, rawItem := range items {
		imageURL := jsonImageURL(rawItem)
		if imageURL == "" {
			return nil, fmt.Errorf("%s[%d].image_url is required", field, index)
		}
		if !strings.HasPrefix(imageURL, "data:") {
			return nil, fmt.Errorf("%s[%d].image_url must be a data URL", field, index)
		}
		header, payload, ok := strings.Cut(imageURL, ",")
		if !ok {
			return nil, fmt.Errorf("%s[%d].image_url must be a valid data URL", field, index)
		}
		if !strings.Contains(strings.ToLower(header), ";base64") {
			return nil, fmt.Errorf("%s[%d].image_url must be base64 encoded", field, index)
		}
		mimeType := strings.TrimSpace(strings.TrimPrefix(header, "data:"))
		if i := strings.Index(mimeType, ";"); i >= 0 {
			mimeType = mimeType[:i]
		}
		raw, decodeErr := base64.StdEncoding.DecodeString(payload)
		if decodeErr != nil || len(raw) == 0 {
			return nil, fmt.Errorf("%s[%d].image_url base64 decode failed", field, index)
		}
		images = append(images, editableImage{
			Data:     raw,
			FileName: fmt.Sprintf("%s-%d.png", strings.TrimSuffix(field, "s"), index+1),
			MimeType: mimeType,
		})
	}
	return images, nil
}

func jsonImageURL(raw any) string {
	switch item := raw.(type) {
	case string:
		return strings.TrimSpace(item)
	case map[string]any:
		if nested, ok := item["image_url"].(map[string]any); ok {
			if value := strings.TrimSpace(fmt.Sprint(nested["url"])); value != "" {
				return value
			}
			if value := strings.TrimSpace(fmt.Sprint(nested["data"])); value != "" {
				return value
			}
		}
		for _, key := range []string{"image_url", "url", "data"} {
			if value := strings.TrimSpace(fmt.Sprint(item[key])); value != "" {
				return value
			}
		}
	}
	return ""
}

func parseMultipartEditImages(form *multipart.Form, fields []string, required bool) ([]editableImage, error) {
	if form == nil {
		if required {
			return nil, fmt.Errorf("image file is required")
		}
		return nil, nil
	}
	uploads := []*multipart.FileHeader{}
	for _, field := range fields {
		uploads = append(uploads, form.File[field]...)
	}
	if len(uploads) == 0 {
		if required {
			return nil, fmt.Errorf("image file is required")
		}
		return nil, nil
	}
	images := make([]editableImage, 0, len(uploads))
	for _, header := range uploads {
		if header == nil {
			continue
		}
		file, err := header.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("image file is empty")
		}
		images = append(images, editableImage{
			Data:     data,
			FileName: defaultString(header.Filename, "image.png"),
			MimeType: defaultString(header.Header.Get("Content-Type"), "image/png"),
		})
	}
	return images, nil
}

func compositeEditMasks(images, masks []editableImage) ([]editableImage, error) {
	if len(masks) == 0 {
		return images, nil
	}
	out := make([]editableImage, 0, len(images))
	for index, item := range images {
		mask := masks[minIndex(index, len(masks)-1)]
		composited, err := compositeSingleMask(item, mask)
		if err != nil {
			return nil, err
		}
		out = append(out, composited)
	}
	return out, nil
}

func compositeSingleMask(item, mask editableImage) (editableImage, error) {
	src, _, err := image.Decode(bytes.NewReader(item.Data))
	if err != nil {
		return editableImage{}, fmt.Errorf("image decode failed: %w", err)
	}
	maskImage, _, err := image.Decode(bytes.NewReader(mask.Data))
	if err != nil {
		return editableImage{}, fmt.Errorf("mask decode failed: %w", err)
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return editableImage{}, fmt.Errorf("image has invalid dimensions")
	}
	rgba := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(rgba, rgba.Bounds(), src, bounds.Min, draw.Src)
	maskBounds := maskImage.Bounds()
	useAlpha := maskUsesAlpha(mask.Data, maskImage)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			alpha := maskAlphaAt(maskImage, maskBounds, x, y, width, height, useAlpha)
			offset := rgba.PixOffset(x, y)
			rgba.Pix[offset+3] = alpha
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, rgba); err != nil {
		return editableImage{}, fmt.Errorf("mask composite encode failed: %w", err)
	}
	return editableImage{
		Data:     buffer.Bytes(),
		FileName: item.FileName,
		MimeType: "image/png",
	}, nil
}

func maskUsesAlpha(data []byte, mask image.Image) bool {
	if pngHasAlpha(data) {
		return true
	}
	switch mask.(type) {
	case *image.Alpha, *image.Alpha16:
		return true
	case *image.Gray, *image.Gray16:
		return false
	}
	bounds := mask.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, a := mask.At(x, y).RGBA()
			if a != 0xffff {
				return true
			}
		}
	}
	return false
}

func pngHasAlpha(data []byte) bool {
	if len(data) < 26 || !bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return false
	}
	colorType := data[25]
	if colorType == 4 || colorType == 6 {
		return true
	}
	offset := 8
	for offset+8 <= len(data) {
		length := int(data[offset])<<24 | int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3])
		if length < 0 || offset+12+length > len(data) {
			return false
		}
		chunkType := string(data[offset+4 : offset+8])
		if chunkType == "tRNS" {
			return true
		}
		if chunkType == "IDAT" {
			return false
		}
		offset += 12 + length
	}
	return false
}

func maskAlphaAt(mask image.Image, bounds image.Rectangle, x, y, width, height int, useAlpha bool) uint8 {
	mx := bounds.Min.X + x*bounds.Dx()/width
	my := bounds.Min.Y + y*bounds.Dy()/height
	r, g, b, a := mask.At(mx, my).RGBA()
	if useAlpha {
		return uint8(a >> 8)
	}
	alpha := (r*299 + g*587 + b*114) / 1000
	return uint8(alpha >> 8)
}

func minIndex(index, max int) int {
	if index < max {
		return index
	}
	return max
}

func parseBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func defaultString(value any, fallback string) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		return fallback
	}
	return text
}

func toProtocolImages(images []editableImage) []protocol.ImageInput {
	out := make([]protocol.ImageInput, 0, len(images))
	for _, item := range images {
		out = append(out, protocol.ImageInput{
			Data:     item.Data,
			FileName: item.FileName,
			MimeType: item.MimeType,
		})
	}
	return out
}

func firstFormValue(r *http.Request, name string) string {
	if r == nil {
		return ""
	}
	if r.MultipartForm != nil {
		if values := r.MultipartForm.Value[name]; len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return strings.TrimSpace(r.FormValue(name))
}
