package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"rua.plus/saber/internal/chat"
)

func resolvePublicHost(ctx context.Context, host string) ([]net.IPAddr, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("media host has no addresses")
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, errors.New("provider reference URL targets non-public host")
		}
	}
	return ips, nil
}
func ownerKey(ctx context.Context) (string, error) {
	id, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return "", errors.New("media artifact requires trusted identity")
	}
	sum := sha256.Sum256([]byte(string(id.Session.Key()) + "\x00" + id.SenderID))
	return hex.EncodeToString(sum[:]), nil
}
func (s *Service) privateDir(ctx context.Context) (string, error) {
	if s.host.StateDir == "" {
		return "", errors.New("media state_dir unavailable")
	}
	owner, err := ownerKey(ctx)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.host.StateDir, "media", owner)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err = os.Chmod(filepath.Join(s.host.StateDir, "media"), 0700); err != nil {
		return "", err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
func (s *Service) saveArtifact(ctx context.Context, a chat.Attachment) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir, err := s.privateDir(ctx)
	if err != nil {
		return "", err
	}
	ext := ".bin"
	switch strings.Split(a.MIMEType, ";")[0] {
	case "audio/mpeg":
		ext = ".mp3"
	case "audio/wav":
		ext = ".wav"
	case "audio/ogg":
		ext = ".ogg"
	case "video/mp4":
		ext = ".mp4"
	case "video/webm":
		ext = ".webm"
	}
	path := filepath.Join(dir, uuid.NewString()+ext)
	if err = os.WriteFile(path, a.Data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

// ReadArtifact 只接受本服务在当前可信会话私有目录生成的文件，不暴露任意宿主路径。
func (s *Service) ReadArtifact(ctx context.Context, path string) (chat.Attachment, error) {
	dir, err := s.privateDir(ctx)
	if err != nil {
		return chat.Attachment{}, err
	}
	if filepath.Dir(path) != dir {
		return chat.Attachment{}, errors.New("media artifact owner mismatch")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return chat.Attachment{}, err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	st, err := root.Lstat(name)
	if err != nil {
		return chat.Attachment{}, err
	}
	if !st.Mode().IsRegular() {
		return chat.Attachment{}, errors.New("media artifact not regular or is symlink")
	}
	f, err := root.Open(name)
	if err != nil {
		return chat.Attachment{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := boundedRead(f, s.cfg.MaxBytes)
	if err != nil {
		return chat.Attachment{}, err
	}
	kind, mt := "audio", "audio/mpeg"
	switch filepath.Ext(path) {
	case ".wav":
		mt = "audio/wav"
	case ".ogg":
		mt = "audio/ogg"
	case ".mp4":
		kind, mt = "video", "video/mp4"
	case ".webm":
		kind, mt = "video", "video/webm"
	}
	a := attachment(kind, mt, data)
	a.Name = name
	return a, nil
}

type generationJob struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	StatusURL string `json:"status_url"`
	ResultURL string `json:"result_url"`
	CancelURL string `json:"cancel_url,omitempty"`
	Status    string `json:"status"`
	Video     bool   `json:"video"`
	FAL       bool   `json:"fal"`
}

func (s *Service) saveJob(ctx context.Context, j generationJob) error {
	dir, err := s.privateDir(ctx)
	if err != nil {
		return err
	}
	if j.ID == "" || strings.ContainsAny(j.ID, "/\\") || j.ID == "." || j.ID == ".." {
		return errors.New("invalid media job id")
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "job-"+j.ID+".json")
	tmp := path + ".tmp-" + uuid.NewString()
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	return os.Rename(tmp, path)
}
func (s *Service) loadJob(ctx context.Context, id string) (generationJob, error) {
	var j generationJob
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return j, errors.New("invalid media job id")
	}
	dir, e := s.privateDir(ctx)
	if e != nil {
		return j, e
	}
	b, e := os.ReadFile(filepath.Join(dir, "job-"+id+".json"))
	if e != nil {
		return j, e
	}
	e = json.Unmarshal(b, &j)
	return j, e
}
func (s *Service) jobTool(ctx context.Context, args map[string]any) (any, error) {
	id := stringArg(args, "request_id")
	j, err := s.loadJob(ctx, id)
	if err != nil {
		return nil, err
	}
	p := s.cfg.Image
	if j.Video {
		p = s.cfg.Video
	}
	if p.Provider != j.Provider {
		return nil, errors.New("media job provider changed; restore configuration before resuming")
	}
	if err = s.authorize(ctx, "media_job", p); err != nil {
		return nil, err
	}
	base, _, err := providerRuntime(p)
	if err != nil {
		return nil, err
	}
	for _, u := range []string{j.StatusURL, j.ResultURL, j.CancelURL} {
		if u != "" && !sameOrigin(base, u) {
			return nil, errors.New("media job endpoint no longer matches trusted configuration")
		}
	}
	ctx, done := s.scope(ctx)
	defer done()
	switch stringArg(args, "action") {
	case "status":
		return j, nil
	case "cancel":
		if j.CancelURL == "" {
			return nil, errors.New("provider has no remote cancel API; local task cancellation does not claim remote billing stopped")
		}
		_, _, err = s.request(ctx, p, "PUT", j.CancelURL, "", nil)
		if err != nil {
			return nil, err
		}
		j.Status = "cancelled"
		if err = s.saveJob(ctx, j); err != nil {
			return nil, err
		}
		return j, nil
	case "resume":
		if j.Status == "cancelled" {
			return nil, errors.New("media job cancelled")
		}
		r, err := s.jsonRequest(ctx, p, "GET", j.StatusURL, nil)
		if err != nil {
			return nil, err
		}
		status := strings.ToUpper(str(r, "status"))
		if status == "COMPLETED" || status == "DONE" || object(r, "video") != nil || len(list(r, "images")) > 0 {
			if j.FAL {
				r, err = s.jsonRequest(ctx, p, "GET", j.ResultURL, nil)
				if err != nil {
					return nil, err
				}
			}
			result, err := s.generationResult(ctx, r, j.Video)
			if err != nil {
				return nil, err
			}
			j.Status = "completed"
			if err = s.saveJob(ctx, j); err != nil {
				return nil, err
			}
			return result, nil
		}
		j.Status = str(r, "status")
		if err = s.saveJob(ctx, j); err != nil {
			return nil, err
		}
		return j, nil
	default:
		return nil, fmt.Errorf("media_job action must be status, resume or cancel")
	}
}
