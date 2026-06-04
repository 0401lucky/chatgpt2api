package protocol

import (
	"context"
	"errors"
	"strings"

	"chatgpt2api-go-backend/internal/account"
)

type ImageInput struct {
	Data     []byte
	FileName string
	MimeType string
}

type ImageGenerator interface {
	GenerateImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string) ([]map[string]any, error)
	EditImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string, images []ImageInput) ([]map[string]any, error)
}

type ImageTokenPool interface {
	AcquireImageToken(ctx context.Context, allow func(map[string]any) bool) (string, func(), error)
	MarkImageResult(accessToken string, success bool) map[string]any
	MarkInvalidToken(accessToken string) map[string]any
}

type ImageAccountPool interface {
	ImageTokenPool
	GetAvailableAccessTokenFor(ctx context.Context, allow func(map[string]any) bool) (string, error)
}

func GenerateImageWithPool(ctx context.Context, image ImageGenerator, accounts ImageTokenPool, prompt, model, size, responseFormat string) ([]map[string]any, error) {
	if image == nil || accounts == nil {
		return nil, errors.New("image generation upstream is not configured")
	}
	return callImageWithPool(ctx, accounts, func(token string) ([]map[string]any, error) {
		return image.GenerateImage(ctx, token, prompt, model, size, responseFormat)
	})
}

func EditImageWithPool(ctx context.Context, image ImageGenerator, accounts ImageTokenPool, prompt, model, size, responseFormat string, images []ImageInput) ([]map[string]any, error) {
	if image == nil || accounts == nil {
		return nil, errors.New("image edit upstream is not configured")
	}
	return callImageWithPool(ctx, accounts, func(token string) ([]map[string]any, error) {
		return image.EditImage(ctx, token, prompt, model, size, responseFormat, images)
	})
}

func callImageWithPool(ctx context.Context, accounts ImageTokenPool, call func(token string) ([]map[string]any, error)) ([]map[string]any, error) {
	tried := map[string]struct{}{}
	var lastErr error
	for {
		token, release, err := accounts.AcquireImageToken(ctx, func(item map[string]any) bool {
			candidate := clean(item["access_token"])
			if candidate == "" {
				return false
			}
			_, seen := tried[candidate]
			return !seen
		})
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		tried[token] = struct{}{}

		data, callErr := call(token)
		release()
		if callErr == nil && len(data) > 0 {
			accounts.MarkImageResult(token, true)
			return data, nil
		}
		if callErr == nil {
			callErr = errors.New("上游没有返回图片，请检查账号额度或稍后重试")
		}

		accounts.MarkImageResult(token, false)
		if account.IsInvalidTokenError(callErr) {
			accounts.MarkInvalidToken(token)
			continue
		}
		lastErr = callErr
		if isRetryableImageAccountError(callErr) {
			continue
		}
		return nil, callErr
	}
}

func isRetryableImageAccountError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "生图超时") ||
		strings.Contains(text, "image poll timeout") ||
		strings.Contains(text, "operation timed out") ||
		strings.Contains(text, "connection timed out") ||
		strings.Contains(text, "read timed out") ||
		strings.Contains(text, "connect timeout")
}
