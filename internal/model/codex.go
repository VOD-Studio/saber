package model

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

func codexAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			Account string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(data, &claims) != nil || strings.ContainsAny(claims.Auth.Account, "\r\n") {
		return ""
	}
	return claims.Auth.Account
}
