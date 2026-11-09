package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rua.plus/saber/internal/config"
)

func oauthRequest(ctx context.Context, cfg config.OAuthConfig, values url.Values, dst any) error {
	if cfg.TokenAuthMethod == "client_secret_basic" || cfg.TokenAuthMethod == "none" {
		values.Del("client_secret")
	}
	var body []byte
	contentType := "application/x-www-form-urlencoded"
	if cfg.TokenEncoding == "json" {
		data := map[string]string{}
		for name := range values {
			data[name] = values.Get(name)
		}
		if cfg.Flow == "openrouter" {
			data = map[string]string{"code": values.Get("code"), "code_verifier": values.Get("code_verifier"), "code_challenge_method": "S256"}
		}
		var err error
		body, err = json.Marshal(data)
		if err != nil {
			return err
		}
		contentType = "application/json"
	} else {
		body = []byte(values.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	if cfg.TokenAuthMethod == "client_secret_basic" {
		req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	}
	if cfg.Flow == "anthropic" {
		req.Header.Set("User-Agent", "axios/1.7.9")
	}
	if cfg.RefreshHeader != "" && values.Get("grant_type") == "refresh_token" {
		req.Header.Set(cfg.RefreshHeader, values.Get("refresh_token"))
	}
	client := &http.Client{Transport: sharedTransport, Timeout: 30 * time.Second, CheckRedirect: rejectProviderRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readBoundedBody(resp.Body)
	if err != nil {
		return err
	}
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &failure)
	if failure.Error != "" {
		return &oauthError{Code: failure.Error}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return protocolError(resp, data)
	}
	if err = json.Unmarshal(data, dst); err != nil {
		return errors.New("invalid OAuth JSON response")
	}
	return nil
}

func miniMaxExpiry(value int64) time.Time {
	if value > time.Now().UnixMilli()/2 {
		return time.UnixMilli(value)
	}
	return time.Now().Add(time.Duration(max(value, 1)) * time.Second)
}

func (a *authManager) miniMaxLogin(ctx context.Context, cfg config.OAuthConfig, pending oauthPending) (map[string]any, error) {
	challenge := sha256.Sum256([]byte(pending.Verifier))
	var response struct {
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Expires         int64  `json:"expired_in"`
		Interval        int    `json:"interval"`
		State           string `json:"state"`
	}
	values := url.Values{"response_type": {"code"}, "client_id": {cfg.ClientID}, "scope": {strings.Join(cfg.Scopes, " ")}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "state": {pending.State}}
	if err := formJSON(ctx, cfg.DeviceURL, values, &response); err != nil {
		return nil, err
	}
	if response.UserCode == "" || response.VerificationURI == "" || response.Expires <= 0 || response.State != pending.State {
		return nil, errors.New("invalid MiniMax authorization response or state mismatch")
	}
	pending.DeviceCode = response.UserCode
	pending.Expires = miniMaxExpiry(response.Expires)
	pending.Interval = max(time.Duration(response.Interval)*time.Millisecond, 2*time.Second)
	a.mu.Lock()
	a.pending[pending.State] = pending
	a.mu.Unlock()
	return map[string]any{"provider": pending.Provider, "state": pending.State, "user_code": response.UserCode, "verification_uri": response.VerificationURI, "expires_at": pending.Expires, "next": "finish"}, nil
}
