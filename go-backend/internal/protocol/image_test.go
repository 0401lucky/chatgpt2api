package protocol

import (
	"context"
	"errors"
	"testing"
)

func TestGenerateImageWithPoolRetriesPollTimeoutOnNextAccount(t *testing.T) {
	accounts := &fakeImagePool{
		tokens: []string{"token-a", "token-b"},
	}
	image := &fakeImageGenerator{
		errs: map[string]error{
			"token-a": errors.New("ChatGPT 生图超时（已等待 120 秒），可能是账号被限流或生图队列拥堵"),
		},
		data: map[string][]map[string]any{
			"token-b": {{"b64_json": "ok"}},
		},
	}

	data, err := GenerateImageWithPool(context.Background(), image, accounts, "prompt", "gpt-image-2", "1:1", "b64_json")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data[0]["b64_json"] != "ok" {
		t.Fatalf("data = %#v", data)
	}
	if accounts.fail["token-a"] != 1 || accounts.success["token-b"] != 1 {
		t.Fatalf("marks success=%#v fail=%#v", accounts.success, accounts.fail)
	}
	if accounts.invalid["token-a"] != 0 {
		t.Fatalf("timeout should not mark account abnormal, invalid=%#v", accounts.invalid)
	}
}

type fakeImageGenerator struct {
	errs map[string]error
	data map[string][]map[string]any
}

func (f *fakeImageGenerator) GenerateImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string) ([]map[string]any, error) {
	if err := f.errs[accessToken]; err != nil {
		return nil, err
	}
	return f.data[accessToken], nil
}

func (f *fakeImageGenerator) EditImage(ctx context.Context, accessToken, prompt, model, size, responseFormat string, images []ImageInput) ([]map[string]any, error) {
	return nil, errors.New("not used")
}

type fakeImagePool struct {
	tokens  []string
	success map[string]int
	fail    map[string]int
	invalid map[string]int
}

func (p *fakeImagePool) AcquireImageToken(ctx context.Context, allow func(map[string]any) bool) (string, func(), error) {
	for _, token := range p.tokens {
		item := map[string]any{"access_token": token}
		if allow == nil || allow(item) {
			return token, func() {}, nil
		}
	}
	return "", nil, errors.New("no available image quota")
}

func (p *fakeImagePool) MarkImageResult(accessToken string, success bool) map[string]any {
	if p.success == nil {
		p.success = map[string]int{}
	}
	if p.fail == nil {
		p.fail = map[string]int{}
	}
	if success {
		p.success[accessToken]++
	} else {
		p.fail[accessToken]++
	}
	return nil
}

func (p *fakeImagePool) MarkInvalidToken(accessToken string) map[string]any {
	if p.invalid == nil {
		p.invalid = map[string]int{}
	}
	p.invalid[accessToken]++
	return nil
}
