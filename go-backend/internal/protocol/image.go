package protocol

import "context"

type ImageInput struct {
	Data     []byte
	FileName string
	MimeType string
}

type ImageGenerator interface {
	GenerateImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string) ([]map[string]any, error)
	EditImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string, images []ImageInput) ([]map[string]any, error)
}

type ImageAccountPool interface {
	GetAvailableAccessTokenFor(ctx context.Context, allow func(map[string]any) bool) (string, error)
	AcquireImageToken(ctx context.Context, allow func(map[string]any) bool) (string, func(), error)
	MarkImageResult(accessToken string, success bool) map[string]any
}
