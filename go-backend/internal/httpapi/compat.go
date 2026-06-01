package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

func parseImageEditRequest(r *http.Request) (prompt, model, size, responseFormat string, stream bool, images []editableImage, err error) {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "application/json") {
		var body map[string]any
		reader := io.LimitReader(r.Body, 32<<20)
		if decodeErr := json.NewDecoder(reader).Decode(&body); decodeErr != nil {
			return "", "", "", "", false, nil, fmt.Errorf("invalid json body")
		}
		prompt = strings.TrimSpace(fmt.Sprint(body["prompt"]))
		if prompt == "" {
			return "", "", "", "", false, nil, fmt.Errorf("prompt is required")
		}
		model = defaultString(body["model"], "gpt-image-2")
		size = strings.TrimSpace(fmt.Sprint(body["size"]))
		responseFormat = defaultString(body["response_format"], "b64_json")
		stream = parseBool(body["stream"])
		images, err = parseJSONEditImages(body["images"])
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return "", "", "", "", false, nil, err
	}
	prompt = strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		return "", "", "", "", false, nil, fmt.Errorf("prompt is required")
	}
	model = defaultString(r.FormValue("model"), "gpt-image-2")
	size = strings.TrimSpace(r.FormValue("size"))
	responseFormat = defaultString(r.FormValue("response_format"), "b64_json")
	stream = parseBool(r.FormValue("stream"))
	images, err = parseMultipartEditImages(r.MultipartForm)
	return
}

func parseJSONEditImages(raw any) ([]editableImage, error) {
	if raw == nil {
		return nil, fmt.Errorf("images is required")
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("images must be a non-empty array")
	}
	images := make([]editableImage, 0, len(items))
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("images[%d] must be an object", index)
		}
		imageURL := strings.TrimSpace(fmt.Sprint(item["image_url"]))
		if imageURL == "" {
			return nil, fmt.Errorf("images[%d].image_url is required", index)
		}
		if !strings.HasPrefix(imageURL, "data:") {
			return nil, fmt.Errorf("images[%d].image_url must be a data URL", index)
		}
		header, payload, ok := strings.Cut(imageURL, ",")
		if !ok {
			return nil, fmt.Errorf("images[%d].image_url must be a valid data URL", index)
		}
		if !strings.Contains(strings.ToLower(header), ";base64") {
			return nil, fmt.Errorf("images[%d].image_url must be base64 encoded", index)
		}
		mimeType := strings.TrimSpace(strings.TrimPrefix(header, "data:"))
		if i := strings.Index(mimeType, ";"); i >= 0 {
			mimeType = mimeType[:i]
		}
		raw, decodeErr := base64.StdEncoding.DecodeString(payload)
		if decodeErr != nil || len(raw) == 0 {
			return nil, fmt.Errorf("images[%d].image_url base64 decode failed", index)
		}
		images = append(images, editableImage{
			Data:     raw,
			FileName: fmt.Sprintf("image-%d.png", index+1),
			MimeType: mimeType,
		})
	}
	return images, nil
}

func parseMultipartEditImages(form *multipart.Form) ([]editableImage, error) {
	if form == nil {
		return nil, fmt.Errorf("image file is required")
	}
	uploads := append([]*multipart.FileHeader{}, form.File["image"]...)
	uploads = append(uploads, form.File["image[]"]...)
	if len(uploads) == 0 {
		return nil, fmt.Errorf("image file is required")
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
