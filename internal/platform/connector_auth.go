package platform

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func (p *connector) authorizeRequest(ctx context.Context, req *http.Request) error {
	token := p.cfg.Token
	switch p.name {
	case "sms":
		req.SetBasicAuth(p.cfg.AppID, p.cfg.Secret)
		return nil
	case "bluebubbles":
		q := req.URL.Query()
		q.Set("password", p.cfg.Password)
		req.URL.RawQuery = q.Encode()
		return nil
	case "photon":
		req.Header.Set("X-Hermes-Sidecar-Token", token)
		return nil
	case "relay":
		req.Header.Set("Authorization", "Bearer "+p.relayToken())
		return nil
	case "weixin":
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("iLink-App-Id", "bot")
		req.Header.Set("iLink-App-ClientVersion", "131584")
		req.Header.Set("X-WECHAT-UIN", base64.StdEncoding.EncodeToString([]byte("1")))
		return nil
	case "dingtalk", "feishu", "qqbot", "teams", "google_chat", "wecom_callback":
		var err error
		token, err = p.accessToken(ctx)
		if err != nil {
			return err
		}
	}
	if token != "" {
		scheme := "Bearer "
		if p.name == "qqbot" {
			scheme = "QQBot "
		}
		req.Header.Set("Authorization", scheme+token)
	}
	if p.name == "dingtalk" {
		req.Header.Set("x-acs-dingtalk-access-token", token)
	}
	if p.name == "wecom_callback" {
		q := req.URL.Query()
		q.Set("access_token", token)
		req.URL.RawQuery = q.Encode()
	}
	return nil
}

func (p *connector) accessToken(ctx context.Context) (string, error) {
	p.authMu.Lock()
	defer p.authMu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenUntil) {
		return p.token, nil
	}
	var endpoint string
	var payload io.Reader
	contentType := "application/json"
	switch p.name {
	case "feishu":
		endpoint = strings.TrimRight(p.cfg.Endpoint, "/") + "/auth/v3/tenant_access_token/internal"
		data, _ := json.Marshal(map[string]string{"app_id": p.cfg.AppID, "app_secret": p.cfg.Secret})
		payload = strings.NewReader(string(data))
	case "dingtalk":
		endpoint = strings.TrimRight(p.cfg.Endpoint, "/") + "/v1.0/oauth2/accessToken"
		data, _ := json.Marshal(map[string]string{"appKey": p.cfg.AppID, "appSecret": p.cfg.Secret})
		payload = strings.NewReader(string(data))
	case "qqbot":
		endpoint = "https://bots.qq.com/app/getAppAccessToken"
		data, _ := json.Marshal(map[string]string{"appId": p.cfg.AppID, "clientSecret": p.cfg.Secret})
		payload = strings.NewReader(string(data))
	case "wecom_callback":
		endpoint = strings.TrimRight(p.cfg.Endpoint, "/") + "/gettoken?corpid=" + url.QueryEscape(p.cfg.TenantID) + "&corpsecret=" + url.QueryEscape(p.cfg.Secret)
	case "teams":
		endpoint = "https://login.microsoftonline.com/" + url.PathEscape(p.cfg.TenantID) + "/oauth2/v2.0/token"
		payload = strings.NewReader(url.Values{"grant_type": {"client_credentials"}, "client_id": {p.cfg.AppID}, "client_secret": {p.cfg.Secret}, "scope": {"https://api.botframework.com/.default"}}.Encode())
		contentType = "application/x-www-form-urlencoded"
	case "google_chat":
		data, err := os.ReadFile(p.cfg.ServiceAccountFile)
		if err != nil {
			return "", fmt.Errorf("读取 Google 服务账号: %w", err)
		}
		var service struct {
			ClientEmail string `json:"client_email"`
			PrivateKey  string `json:"private_key"`
			TokenURI    string `json:"token_uri"`
		}
		if err = json.Unmarshal(data, &service); err != nil {
			return "", err
		}
		if service.TokenURI != "https://oauth2.googleapis.com/token" {
			return "", errors.New("服务账号 token_uri 必须为官方 OAuth 地址")
		}
		block, _ := pem.Decode([]byte(service.PrivateKey))
		if block == nil {
			return "", errors.New("服务账号私钥无效")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("服务账号私钥必须为 RSA 类型")
		}
		now := time.Now().Unix()
		assertion, err := signJWT(rsaKey, map[string]any{"iss": service.ClientEmail, "scope": "https://www.googleapis.com/auth/chat.bot", "aud": service.TokenURI, "iat": now, "exp": now + 3600})
		if err != nil {
			return "", err
		}
		endpoint = service.TokenURI
		contentType = "application/x-www-form-urlencoded"
		payload = strings.NewReader(url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}.Encode())
	default:
		return p.cfg.Token, nil
	}
	method := http.MethodPost
	if p.name == "wecom_callback" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	response, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("平台凭据交换失败: %s", p.redact(err.Error()))
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode != 200 {
		return "", fmt.Errorf("平台凭据交换 HTTP %d", response.StatusCode)
	}
	var out map[string]any
	if err = json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if apiErr(out) {
		return "", fmt.Errorf("平台凭据交换被拒绝: %s", p.redact(string(data)))
	}
	token := first(out, "access_token", "tenant_access_token", "accessToken")
	if token == "" {
		return "", errors.New("平台凭据交换未返回 access token")
	}
	ttl := 3600
	for _, k := range []string{"expires_in", "expire", "expireIn"} {
		if n, ok := out[k].(float64); ok && n > 0 {
			ttl = int(n)
		}
	}
	p.token = token
	p.redactedToken.Store(token)
	p.tokenUntil = time.Now().Add(time.Duration(max(1, ttl-60)) * time.Second)
	return token, nil
}
func signJWT(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (p *connector) verifyJWT(ctx context.Context, token string) error {
	pieces := strings.Split(strings.TrimPrefix(token, "Bearer "), ".")
	if len(pieces) != 3 {
		return errors.New("回调缺少有效 JWT")
	}
	var header map[string]any
	data, err := base64.RawURLEncoding.DecodeString(pieces[0])
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &header); err != nil {
		return err
	}
	if str(header["alg"]) != "RS256" {
		return errors.New("回调仅接受 RS256 JWT")
	}
	data, err = base64.RawURLEncoding.DecodeString(pieces[1])
	if err != nil {
		return err
	}
	var claims map[string]any
	if err = json.Unmarshal(data, &claims); err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	exp, _ := claims["exp"].(float64)
	nbf, _ := claims["nbf"].(float64)
	if exp <= now || nbf > now+60 {
		return errors.New("回调 JWT 过期或尚未生效")
	}
	aud := str(claims["aud"])
	if aud != p.cfg.AppID {
		return errors.New("回调 JWT audience 不匹配")
	}
	jwks := "https://www.googleapis.com/oauth2/v3/certs"
	issuer := str(claims["iss"])
	if p.name == "teams" {
		jwks = "https://login.botframework.com/v1/.well-known/keys"
		if issuer != "https://api.botframework.com" {
			return errors.New("teams JWT issuer 不匹配")
		}
	} else {
		if issuer != "chat@system.gserviceaccount.com" && issuer != "https://accounts.google.com" && issuer != "accounts.google.com" {
			return errors.New("google_chat JWT issuer 不匹配")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwks, nil)
	if err != nil {
		return err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return errors.New("读取官方 JWT 公钥失败")
	}
	var keys struct {
		Keys []struct {
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		}
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&keys); err != nil {
		return err
	}
	signature, err := base64.RawURLEncoding.DecodeString(pieces[2])
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(pieces[0] + "." + pieces[1]))
	for _, key := range keys.Keys {
		if key.Kid != str(header["kid"]) {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil {
			return err
		}
		e, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil {
			return err
		}
		return rsa.VerifyPKCS1v15(&rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, crypto.SHA256, digest[:], signature)
	}
	return errors.New("回调 JWT 公钥未知")
}
func equalSecret(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func verifyMAC(secret string, body []byte, signature string, encoding string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	var got []byte
	var err error
	if encoding == "base64" {
		got, err = base64.StdEncoding.DecodeString(signature)
	} else {
		got, err = decodeHex(strings.TrimPrefix(signature, "sha256="))
	}
	return err == nil && hmac.Equal(expected, got)
}
func decodeHex(s string) ([]byte, error) {
	const digits = "0123456789abcdef"
	if len(s)%2 != 0 {
		return nil, errors.New("无效 hex")
	}
	out := make([]byte, len(s)/2)
	for i := range out {
		hi := strings.IndexByte(digits, s[i*2])
		lo := strings.IndexByte(digits, s[i*2+1])
		if hi < 0 || lo < 0 {
			return nil, errors.New("无效 hex")
		}
		out[i] = byte(hi*16 + lo)
	}
	return out, nil
}
