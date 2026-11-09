package plugins

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

func gitEnvironment() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "SSH_AUTH_SOCK=" + os.Getenv("SSH_AUTH_SOCK"), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
}
func gitOnce(ctx context.Context, dir string, extra []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitEnvironment(), extra...)
	return cmd.Output()
}
func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := gitOnce(ctx, dir, nil, args...)
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	source := ""
	if len(args) > 0 && args[0] == "clone" && len(args) >= 2 {
		source = args[len(args)-2]
	} else {
		if remote, err := gitOnce(ctx, dir, nil, "remote", "get-url", "origin"); err == nil {
			source = strings.TrimSpace(string(remote))
		}
	}
	endpoint, parseErr := url.Parse(source)
	if parseErr != nil || endpoint.Scheme != "https" || endpoint.User != nil {
		return nil, errors.New("git operation failed; verify exact source/revision and noninteractive ssh-agent credentials")
	}
	username, password := "", ""
	if strings.EqualFold(endpoint.Hostname(), "github.com") {
		password = os.Getenv("GITHUB_TOKEN")
		if password == "" {
			password = os.Getenv("GH_TOKEN")
		}
		if password == "" {
			cmd := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", endpoint.Hostname())
			cmd.Env = gitEnvironment()
			if token, err := cmd.Output(); err == nil {
				password = strings.TrimSpace(string(token))
				clear(token)
			}
		}
		if password != "" {
			username = "x-access-token"
		}
	}
	if password == "" {
		cmd := exec.CommandContext(ctx, "git", "credential", "fill")
		cmd.Dir = dir
		cmd.Env = gitEnvironment()
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + endpoint.Host + "\npath=" + strings.TrimLeft(endpoint.Path, "/") + "\n\n")
		if credential, err := cmd.Output(); err == nil {
			scanner := bufio.NewScanner(strings.NewReader(string(credential)))
			for scanner.Scan() {
				pair := strings.SplitN(scanner.Text(), "=", 2)
				if len(pair) == 2 {
					switch pair[0] {
					case "username":
						username = pair[1]
					case "password":
						password = pair[1]
					}
				}
			}
			clear(credential)
		}
	}
	if username == "" || password == "" {
		return nil, errors.New("plugin private source unavailable; configure GITHUB_TOKEN/GH_TOKEN, gh login or a git credential helper for that host")
	}
	header := "AUTHORIZATION: Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	extra := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http." + endpoint.Scheme + "://" + endpoint.Host + "/.extraheader", "GIT_CONFIG_VALUE_0=" + header}
	out, err = gitOnce(ctx, dir, extra, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("authenticated plugin git operation failed; source credential or exact revision rejected")
	}
	return out, nil
}
