package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"rua.plus/saber/internal/config"
)

func googleTokenSource(path string) (func(context.Context) (string, error), error) {
	if path == "" {
		return nil, errors.New("vertex requires explicit google.credentials_file or OAuth credentials")
	}
	var raw map[string]json.RawMessage
	if err := readPrivateJSON(path, &raw); err != nil {
		return nil, err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	cfg, err := google.JWTConfigFromJSON(data, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, err
	}
	// 不采用凭据 JSON 里的任意 token_uri，避免把服务账号签名发往非 Google 服务。
	if cfg.TokenURL != "https://oauth2.googleapis.com/token" && cfg.TokenURL != "https://accounts.google.com/o/oauth2/token" {
		return nil, errors.New("unsupported Google service-account token URI")
	}
	var cached *oauth2.Token
	var mu sync.Mutex
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != nil && cached.Valid() && time.Until(cached.Expiry) > 2*time.Minute {
			return cached.AccessToken, nil
		}
		client := &http.Client{Transport: sharedTransport, Timeout: 30 * time.Second, CheckRedirect: rejectProviderRedirect}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
		token, err := cfg.TokenSource(ctx).Token()
		if err != nil {
			return "", errors.New("google service-account token exchange failed")
		}
		cached = token
		return token.AccessToken, nil
	}, nil
}

func configureGoogleEndpoint(model *config.ModelConfig, cfg config.GoogleProviderConfig) error {
	if cfg.Project == "" || (model.Provider != "vertex" && model.Provider != "google-vertex") {
		return nil
	}
	if strings.Contains(model.BaseURL, "/projects/") {
		return nil
	}
	region := cfg.Region
	if region == "" {
		region = "global"
	}
	if strings.ContainsAny(region, "/:?#.") {
		return errors.New("invalid Google region")
	}
	base, err := url.Parse(model.BaseURL)
	if err != nil {
		return err
	}
	if base.Hostname() == "aiplatform.googleapis.com" && region != "global" {
		base.Host = region + "-aiplatform.googleapis.com"
	}
	prefix := strings.TrimRight(base.String(), "/")
	if !strings.Contains(base.Path, "/v1") {
		prefix += "/v1beta1"
	}
	prefix += "/projects/" + url.PathEscape(cfg.Project) + "/locations/" + url.PathEscape(region)
	if effectiveAPI(model.Provider, model.API) == "google-gemini" {
		prefix += "/publishers/google"
	} else {
		prefix += "/endpoints/openapi"
	}
	model.BaseURL = prefix
	return nil
}
