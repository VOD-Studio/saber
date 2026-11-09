package vault

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"rua.plus/saber/internal/config"
)

func (s *Store) managerConfig(name string) config.PasswordManagerConfig {
	if name == "onepassword" {
		return s.cfg.OnePassword
	}
	return s.cfg.Bitwarden
}
func (s *Store) managerEnabled(name string) bool {
	return name == "onepassword" && s.cfg.OnePassword.Enabled || name == "bitwarden" && s.cfg.Bitwarden.Enabled
}
func (s *Store) managerAllowed(name, who string) bool {
	return s.managerEnabled(name) && contains(s.managerConfig(name).AllowedUsers, who)
}
func (s *Store) managerToken(who, name string) string {
	s.managerMu.Lock()
	defer s.managerMu.Unlock()
	key := who + ":" + name
	v, ok := s.sessions[key]
	if !ok {
		return ""
	}
	if time.Since(v.Touched) > time.Duration(s.cfg.IdleTimeoutSeconds)*time.Second {
		delete(s.sessions, key)
		return ""
	}
	v.Touched = time.Now()
	s.sessions[key] = v
	return v.Token
}
func (s *Store) runManager(ctx context.Context, name, who string, args []string, stdin io.Reader, extraEnv []string) ([]byte, error) {
	if !s.managerAllowed(name, who) {
		return nil, errors.New("password manager source is disabled or not authorized for this trusted user")
	}
	cfg := s.managerConfig(name)
	if cfg.Command == "" {
		return nil, errors.New("password manager command is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if name == "onepassword" && cfg.Account != "" {
		args = append([]string{"--account", cfg.Account}, args...)
	}
	cmd := exec.CommandContext(ctx, cfg.Command, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER"), "NO_COLOR=1"}
	if cfg.TokenEnv != "" {
		if token := os.Getenv(cfg.TokenEnv); token != "" {
			key := "OP_SERVICE_ACCOUNT_TOKEN"
			if name == "bitwarden" {
				key = "BW_SESSION"
			}
			cmd.Env = append(cmd.Env, key+"="+token)
		}
	}
	if token := s.managerToken(who, name); token != "" {
		key := "OP_SESSION"
		if name == "bitwarden" {
			key = "BW_SESSION"
		} else if cfg.Account != "" {
			key += "_" + cfg.Account
		}
		cmd.Env = append(cmd.Env, key+"="+token)
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdin = stdin
	out, err := cmd.Output()
	if err != nil {
		clear(out)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("password manager unavailable or locked; unlock using a trusted interactive vault command")
	}
	if len(out) > 8<<20 {
		clear(out)
		return nil, errors.New("password manager output exceeds limit")
	}
	return out, nil
}

// Unlock 输入只进入子进程 stdin 或私有环境，token 按用户隔离并在空闲后过期。
func (s *Store) Unlock(ctx context.Context, name, password string) error {
	who, err := owner(ctx)
	if err != nil {
		return err
	}
	if name != "onepassword" && name != "bitwarden" {
		return errors.New("unknown password manager")
	}
	var out []byte
	if name == "onepassword" {
		out, err = s.runManager(ctx, name, who, []string{"signin", "--raw"}, strings.NewReader(password+"\n"), nil)
	} else {
		out, err = s.runManager(ctx, name, who, []string{"unlock", "--passwordenv", "SABER_VAULT_MASTER", "--raw"}, nil, []string{"SABER_VAULT_MASTER=" + password})
	}
	if err != nil {
		return err
	}
	defer clear(out)
	token := strings.TrimSpace(string(out))
	if token == "" {
		return errors.New("password manager did not return an unlock session")
	}
	s.managerMu.Lock()
	s.sessions[who+":"+name] = managerSession{token, time.Now()}
	s.managerMu.Unlock()
	s.remember(map[string]string{"master": password, "token": token})
	return nil
}

type opItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Category   string `json:"category"`
	Additional string `json:"additional_information"`
	Vault      struct {
		ID string `json:"id"`
	} `json:"vault"`
	URLs []struct {
		Href string `json:"href"`
	} `json:"urls"`
	Fields []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Value string `json:"value"`
		Type  string `json:"type"`
	} `json:"fields"`
}
type bwItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  int    `json:"type"`
	Login *struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
		URIs     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	} `json:"login"`
}

func (s *Store) managerItems(ctx context.Context, name, who string, secrets bool, id string) ([]diskItem, error) {
	if secrets {
		listed, err := s.managerItems(ctx, name, who, false, "")
		if err != nil {
			return nil, err
		}
		found := false
		for _, item := range listed {
			if item.Handle == name+":"+id {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("password manager login is not in the current authorized listing")
		}
	}
	var args []string
	if name == "onepassword" {
		if secrets {
			parts := strings.SplitN(id, "/", 2)
			itemID := parts[len(parts)-1]
			args = []string{"item", "get", itemID, "--format", "json"}
			if len(parts) == 2 {
				args = append(args, "--vault", parts[0])
			}
		} else {
			args = []string{"item", "list", "--categories", "Login", "--format", "json"}
		}
	} else {
		if secrets {
			args = []string{"get", "item", id}
		} else {
			args = []string{"list", "items"}
		}
	}
	out, err := s.runManager(ctx, name, who, args, nil, nil)
	if err != nil {
		return nil, err
	}
	defer clear(out)
	items := []diskItem{}
	if name == "onepassword" {
		var parsed []opItem
		if secrets {
			var item opItem
			if json.Unmarshal(out, &item) != nil {
				return nil, errors.New("invalid 1Password item")
			}
			parsed = []opItem{item}
		} else if json.Unmarshal(out, &parsed) != nil {
			return nil, errors.New("invalid 1Password list")
		}
		for _, item := range parsed {
			handle := name + ":" + item.ID
			if item.Vault.ID != "" {
				handle = name + ":" + item.Vault.ID + "/" + item.ID
			}
			meta := Metadata{Handle: handle, Kind: "login", Label: item.Title, Identifier: item.Additional, Source: name}
			fields := map[string]string{}
			for _, u := range item.URLs {
				if origin, err := NormalizeOrigin(u.Href); err == nil {
					meta.Origins = append(meta.Origins, origin)
				}
			}
			for _, f := range item.Fields {
				switch {
				case f.ID == "username":
					meta.Identifier = f.Value
				case f.ID == "password" && secrets:
					fields["password"] = f.Value
				case f.Type == "OTP":
					meta.TOTP = true
					if secrets {
						fields["totp_secret"] = f.Value
					}
				}
			}
			if len(meta.Origins) > 0 {
				items = append(items, diskItem{meta, fields, who})
			}
		}
	} else {
		var parsed []bwItem
		if secrets {
			var item bwItem
			if json.Unmarshal(out, &item) != nil {
				return nil, errors.New("invalid Bitwarden item")
			}
			parsed = []bwItem{item}
		} else if json.Unmarshal(out, &parsed) != nil {
			return nil, errors.New("invalid Bitwarden list")
		}
		for _, item := range parsed {
			if item.Login == nil || item.Type != 1 {
				continue
			}
			meta := Metadata{Handle: name + ":" + item.ID, Kind: "login", Label: item.Name, Identifier: item.Login.Username, Source: name, TOTP: item.Login.TOTP != ""}
			for _, u := range item.Login.URIs {
				if origin, err := NormalizeOrigin(u.URI); err == nil {
					meta.Origins = append(meta.Origins, origin)
				}
			}
			fields := map[string]string{}
			if secrets {
				fields["password"] = item.Login.Password
				fields["totp_secret"] = item.Login.TOTP
			}
			if len(meta.Origins) > 0 {
				items = append(items, diskItem{meta, fields, who})
			}
		}
	}
	return items, nil
}
