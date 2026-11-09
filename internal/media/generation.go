package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	_ "golang.org/x/image/webp"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func (s *Service) generate(ctx context.Context, name string, args map[string]any) (toolkit.Result, error) {
	var empty toolkit.Result
	p := s.cfg.Image
	video := strings.Contains(name, "video")
	if video {
		p = s.cfg.Video
	}
	if err := s.authorize(ctx, name, p); err != nil {
		return empty, err
	}
	ctx, done := s.scope(ctx)
	defer done()
	prompt := stringArg(args, "prompt")
	if prompt == "" && name != "image_upscale" {
		return empty, errors.New("generation prompt required")
	}
	payload := options(p)
	if prompt != "" {
		payload["prompt"] = prompt
	} else if str(payload, "prompt") == "" {
		payload["prompt"] = "Enhance image resolution and detail while preserving subject and composition."
	}
	for _, k := range []string{"aspect_ratio", "seed", "duration", "resolution", "size", "quality", "negative_prompt", "num_images"} {
		if v, ok := args[k]; ok {
			payload[k] = v
		}
	}
	var refs []string
	if u := stringArg(args, "image_url"); u != "" {
		refs = append(refs, u)
	}
	if values, ok := args["reference_image_urls"].([]any); ok {
		for _, v := range values {
			u, ok := v.(string)
			if !ok {
				return empty, errors.New("reference_image_urls must be strings")
			}
			refs = append(refs, u)
		}
	}
	if len(refs) > 8 {
		return empty, errors.New("too many generation reference images")
	}
	for i, u := range refs {
		data, _, e := s.load(ctx, chat.Attachment{Kind: "image", URL: u})
		if e != nil {
			return empty, e
		}
		_, format, e := image.DecodeConfig(bytes.NewReader(data))
		if e != nil {
			return empty, fmt.Errorf("reference image invalid: %w", e)
		}
		refs[i] = attachment("image", "image/"+format, data).URL
	}
	if len(refs) > 0 {
		payload["image_url"] = refs[0]
		payload["reference_image_urls"] = refs
	}
	if name == "image_edit" && len(refs) == 0 {
		return empty, errors.New("image_edit requires image_url")
	}
	if name == "image_upscale" && len(refs) == 0 {
		return empty, errors.New("image_upscale requires image_url")
	}
	if name == "xai_video_edit" || name == "xai_video_extend" {
		u := stringArg(args, "video_url")
		parsed, e := url.Parse(u)
		if e != nil || validateURL(parsed) != nil || parsed.Scheme != "https" {
			return empty, errors.New("xAI edit/extend requires public HTTPS video_url")
		}
		ips, e := resolvePublicHost(ctx, parsed.Hostname())
		if e != nil {
			return empty, e
		}
		_ = ips
		payload["video"] = map[string]any{"url": u}
	}
	if p.Provider == "command" || p.Provider == "plugin" {
		payload["operation"] = name
		payload["model"] = p.Model
		r, e := s.command(ctx, p, payload)
		if e != nil {
			return empty, e
		}
		return s.generationResult(ctx, r, video)
	}
	base, _, err := providerRuntime(p)
	if err != nil {
		return empty, err
	}
	var response map[string]any
	switch p.Provider {
	case "fal":
		endpoint := p.Model
		if endpoint == "" {
			if video {
				return empty, errors.New("FAL video model required")
			}
			endpoint = "fal-ai/flux/schnell"
		}
		if name == "image_edit" || (!video && name == "image_generate" && len(refs) > 0) {
			endpoint = p.EditModel
		}
		if name == "image_upscale" {
			endpoint = p.UpscaleModel
		}
		if endpoint == "" {
			return empty, fmt.Errorf("%s references/edit/upscale require configured provider endpoint", name)
		}
		if !video && (strings.Contains(endpoint, "flux") || strings.Contains(endpoint, "z-image") || p.Options["size_parameter"] == "image_size") {
			if ratio := stringArg(args, "aspect_ratio"); ratio != "" {
				width, height, ok := imageDimensions(ratio)
				if !ok {
					return empty, errors.New("unsupported image aspect_ratio")
				}
				payload["image_size"] = map[string]any{"width": width, "height": height}
			}
			delete(payload, "aspect_ratio")
		}
		delete(payload, "size_parameter")
		delete(payload, "reference_image_urls")
		if len(refs) > 0 {
			parameter := "image_urls"
			if video || name == "image_upscale" {
				parameter = "image_url"
			}
			if configured, ok := p.Options["reference_parameter"].(string); ok && configured != "" {
				parameter = configured
			}
			delete(payload, "image_url")
			delete(payload, "image_urls")
			if parameter == "image_url" {
				if len(refs) > 1 {
					return empty, errors.New("configured FAL endpoint accepts one image_url; choose an array-capable reference_parameter explicitly")
				}
				payload[parameter] = refs[0]
			} else {
				payload[parameter] = refs
			}
		}
		delete(payload, "reference_parameter")
		job, e := s.jsonRequest(ctx, p, "POST", base+"/"+strings.TrimLeft(endpoint, "/"), payload)
		if e != nil {
			return empty, e
		}
		return s.pollGeneration(ctx, p, base, job, true, video)
	case "xai":
		if len(refs) > 1 {
			return empty, errors.New("configured xAI image/video protocol accepts one source image")
		}
		payload["model"] = model(p, "grok-imagine-image")
		if n, ok := payload["num_images"]; ok {
			payload["n"] = n
		}
		delete(payload, "num_images")
		if ratio := stringArg(args, "aspect_ratio"); ratio != "" {
			payload["aspect_ratio"] = nativeAspect(ratio)
		}
		path := "/images/generations"
		if len(refs) > 0 {
			path = "/images/edits"
			payload["image"] = map[string]any{"url": refs[0]}
		}
		delete(payload, "reference_image_urls")
		delete(payload, "image_url")
		if video {
			payload["model"] = model(p, "grok-imagine-video")
			path = "/videos/generations"
			if len(refs) > 0 {
				payload["image"] = map[string]any{"url": refs[0]}
			}
		}
		if name == "xai_video_edit" {
			path = "/videos/edits"
		}
		if name == "xai_video_extend" {
			path = "/videos/extensions"
		}
		response, err = s.jsonRequest(ctx, p, "POST", base+path, payload)
		if err == nil && video {
			return s.pollGeneration(ctx, p, base, response, false, true)
		}
	case "openai":
		payload["model"] = model(p, "gpt-image-1")
		if n, ok := payload["num_images"]; ok {
			payload["n"] = n
		}
		delete(payload, "num_images")
		if stringArg(args, "size") == "" {
			switch stringArg(args, "aspect_ratio") {
			case "landscape", "16:9", "3:2":
				payload["size"] = "1536x1024"
			case "portrait", "9:16", "2:3":
				payload["size"] = "1024x1536"
			case "square", "1:1":
				payload["size"] = "1024x1024"
			}
		}
		delete(payload, "aspect_ratio")
		delete(payload, "image_url")
		delete(payload, "reference_image_urls")
		if len(refs) == 0 {
			response, err = s.jsonRequest(ctx, p, "POST", base+"/images/generations", payload)
		} else {
			var b bytes.Buffer
			w := multipart.NewWriter(&b)
			for k, v := range payload {
				if e := w.WriteField(k, fmt.Sprint(v)); e != nil {
					return empty, e
				}
			}
			for _, ref := range refs {
				data, mt, e := s.load(ctx, chat.Attachment{Kind: "image", URL: ref})
				if e != nil {
					return empty, e
				}
				header := make(textproto.MIMEHeader)
				header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image[]"; filename="source.%s"`, strings.TrimPrefix(mt, "image/")))
				header.Set("Content-Type", mt)
				part, e := w.CreatePart(header)
				if e != nil {
					return empty, e
				}
				if _, e = part.Write(data); e != nil {
					return empty, e
				}
			}
			if e := w.Close(); e != nil {
				return empty, e
			}
			raw, _, e := s.request(ctx, p, "POST", base+"/images/edits", w.FormDataContentType(), &b)
			if e != nil {
				return empty, e
			}
			err = json.Unmarshal(raw, &response)
		}
	case "openrouter":
		parts := []any{map[string]any{"type": "text", "text": prompt}}
		for _, r := range refs {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": r}})
		}
		payload = options(p)
		payload["model"] = p.Model
		if p.Model == "" {
			return empty, errors.New("OpenRouter image model required")
		}
		payload["messages"] = []any{map[string]any{"role": "user", "content": parts}}
		payload["modalities"] = []string{"image", "text"}
		if ratio := stringArg(args, "aspect_ratio"); ratio != "" {
			imageConfig := map[string]any{}
			for k, v := range object(payload, "image_config") {
				imageConfig[k] = v
			}
			imageConfig["aspect_ratio"] = nativeAspect(ratio)
			payload["image_config"] = imageConfig
		}
		response, err = s.jsonRequest(ctx, p, "POST", base+"/chat/completions", payload)
	case "gemini":
		parts := []any{map[string]any{"text": prompt}}
		for _, r := range refs {
			data, mt, e := s.load(ctx, chat.Attachment{Kind: "image", URL: r})
			if e != nil {
				return empty, e
			}
			parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mt, "data": base64.StdEncoding.EncodeToString(data)}})
		}
		generationConfig := map[string]any{}
		if configured, ok := p.Options["generation_config"].(map[string]any); ok {
			for k, v := range configured {
				generationConfig[k] = v
			}
		}
		generationConfig["responseModalities"] = []string{"TEXT", "IMAGE"}
		if ratio := stringArg(args, "aspect_ratio"); ratio != "" {
			imageConfig := map[string]any{}
			for k, v := range object(generationConfig, "imageConfig") {
				imageConfig[k] = v
			}
			imageConfig["aspectRatio"] = nativeAspect(ratio)
			generationConfig["imageConfig"] = imageConfig
		}
		response, err = s.jsonRequest(ctx, p, "POST", base+"/models/"+url.PathEscape(model(p, "gemini-2.5-flash-image"))+":generateContent", map[string]any{"contents": []any{map[string]any{"parts": parts}}, "generationConfig": generationConfig})
	default:
		return empty, fmt.Errorf("media generation provider %s unsupported", p.Provider)
	}
	if err != nil {
		return empty, err
	}
	return s.generationResult(ctx, response, video)
}
func (s *Service) pollGeneration(ctx context.Context, p config.MediaProviderConfig, base string, job map[string]any, fal, video bool) (toolkit.Result, error) {
	id := str(job, "request_id")
	if id == "" {
		return s.generationResult(ctx, job, video)
	}
	statusURL := base + "/videos/" + url.PathEscape(id)
	resultURL := statusURL
	cancelURL := ""
	if fal {
		statusURL = str(job, "status_url")
		resultURL = str(job, "response_url")
		cancelURL = str(job, "cancel_url")
		if statusURL == "" || resultURL == "" {
			return toolkit.Result{}, errors.New("FAL response lacks polling URLs")
		}
	}
	for _, u := range []string{statusURL, resultURL, cancelURL} {
		if u != "" && !sameOrigin(base, u) {
			return toolkit.Result{}, errors.New("media provider returned cross-origin job URL")
		}
	}
	receipt := generationJob{ID: id, Provider: p.Provider, StatusURL: statusURL, ResultURL: resultURL, CancelURL: cancelURL, Status: "submitted", Video: video, FAL: fal}
	if err := s.saveJob(ctx, receipt); err != nil {
		return toolkit.Result{Data: map[string]any{"request_id": id}}, fmt.Errorf("media job submitted but receipt persistence failed: %w", err)
	}
	for {
		r, err := s.jsonRequest(ctx, p, "GET", statusURL, nil)
		if err != nil {
			if ctx.Err() != nil {
				return s.interruptGeneration(ctx, p, receipt, err)
			}
			return toolkit.Result{Data: map[string]any{"request_id": id, "status": "unknown", "provider": p.Provider}}, err
		}
		status := strings.ToUpper(str(r, "status"))
		receipt.Status = status
		if err = s.saveJob(ctx, receipt); err != nil {
			return toolkit.Result{Data: map[string]any{"request_id": id}}, err
		}
		if status == "FAILED" || status == "ERROR" || status == "CANCELLED" || status == "EXPIRED" {
			return toolkit.Result{Data: map[string]any{"request_id": id, "status": status}}, fmt.Errorf("media generation %s: %v", status, r)
		}
		if status == "COMPLETED" || status == "DONE" || object(r, "video") != nil || len(list(r, "images")) > 0 {
			if fal {
				r, err = s.jsonRequest(ctx, p, "GET", resultURL, nil)
				if err != nil {
					return toolkit.Result{}, err
				}
			}
			out, err := s.generationResult(ctx, r, video)
			if data, ok := out.Data.(map[string]any); ok {
				data["request_id"] = id
			}
			return out, err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return s.interruptGeneration(ctx, p, receipt, ctx.Err())
		case <-timer.C:
		}
	}
}
func (s *Service) interruptGeneration(ctx context.Context, p config.MediaProviderConfig, receipt generationJob, cause error) (toolkit.Result, error) {
	receipt.Status = "interrupted"
	var cancelErr error
	if receipt.CancelURL != "" {
		c, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, _, cancelErr = s.request(c, p, "PUT", receipt.CancelURL, "", nil)
		if cancelErr == nil {
			receipt.Status = "cancelled"
		}
		stop()
	}
	saveErr := s.saveJob(context.WithoutCancel(ctx), receipt)
	return toolkit.Result{Data: map[string]any{"request_id": receipt.ID, "status": receipt.Status, "provider": p.Provider}}, errors.Join(cause, cancelErr, saveErr)
}
func sameOrigin(base, target string) bool {
	b, e := url.Parse(base)
	if e != nil {
		return false
	}
	u, e := url.Parse(target)
	return e == nil && u.User == nil && u.Scheme == b.Scheme && u.Host == b.Host
}
func (s *Service) generationResult(ctx context.Context, r map[string]any, video bool) (toolkit.Result, error) {
	var out toolkit.Result
	out.Data = map[string]any{"provider_result": r}
	if success, ok := r["success"].(bool); ok && !success {
		return out, fmt.Errorf("generation provider failed: %v", r["error"])
	}
	if video {
		u := str(r, "video")
		if v := object(r, "video"); v != nil {
			u = str(v, "url")
		}
		if u == "" {
			u = str(r, "url")
		}
		if u == "" {
			return out, errors.New("video provider returned no video")
		}
		b, _, err := s.load(ctx, chat.Attachment{Kind: "video", URL: u})
		if err != nil {
			return out, err
		}
		var mt string
		if len(b) >= 12 && bytes.Equal(b[4:8], []byte("ftyp")) {
			mt = "video/mp4"
		} else if len(b) >= 4 && bytes.Equal(b[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
			mt = "video/webm"
		} else {
			return out, errors.New("video provider returned no valid MP4/WebM container")
		}
		a := attachment("video", mt, b)
		out.Data = map[string]any{"attachment": a, "public_url": u}
		path, err := s.saveArtifact(ctx, a)
		if err != nil {
			return out, err
		}
		out.Files = []string{path}
		return out, nil
	}
	var urls []string
	for _, key := range []string{"images", "data"} {
		for _, v := range list(r, key) {
			m, _ := v.(map[string]any)
			if u, ok := v.(string); ok {
				urls = append(urls, u)
			}
			if u := str(m, "url"); u != "" {
				urls = append(urls, u)
			}
			if b := str(m, "b64_json"); b != "" {
				urls = append(urls, "data:image/png;base64,"+b)
			}
		}
	}
	if u := str(r, "image"); u != "" {
		urls = append(urls, u)
	}
	for _, v := range list(r, "choices") {
		c, _ := v.(map[string]any)
		for _, im := range list(object(c, "message"), "images") {
			m, _ := im.(map[string]any)
			if u := str(object(m, "image_url"), "url"); u != "" {
				urls = append(urls, u)
			}
		}
	}
	for _, v := range list(r, "candidates") {
		c, _ := v.(map[string]any)
		for _, part := range list(object(c, "content"), "parts") {
			m, _ := part.(map[string]any)
			inline := object(m, "inlineData")
			if inline != nil {
				urls = append(urls, "data:"+str(inline, "mimeType")+";base64,"+str(inline, "data"))
			}
		}
	}
	if len(urls) == 0 {
		return out, errors.New("image provider returned no images")
	}
	if len(urls) > 16 {
		return out, errors.New("image provider returned too many images")
	}
	for _, u := range urls {
		b, _, e := s.load(ctx, chat.Attachment{Kind: "image", URL: u})
		if e != nil {
			return out, e
		}
		_, format, e := image.DecodeConfig(bytes.NewReader(b))
		if e != nil {
			return out, fmt.Errorf("generated image invalid: %w", e)
		}
		mt := "image/" + format
		out.Images = append(out.Images, attachment("image", mt, b).URL)
	}
	// 已验证像素才进入模型，不把供应商 JSON 里的任意链接当作可信图片。
	out.Data = map[string]any{"images": len(out.Images)}
	return out, nil
}
func imageDimensions(ratio string) (int, int, bool) {
	switch strings.ToLower(ratio) {
	case "square", "1:1":
		return 1024, 1024, true
	case "landscape", "16:9":
		return 1280, 720, true
	case "portrait", "9:16":
		return 720, 1280, true
	case "4:3":
		return 1024, 768, true
	case "3:4":
		return 768, 1024, true
	case "3:2":
		return 1152, 768, true
	case "2:3":
		return 768, 1152, true
	case "21:9":
		return 1344, 576, true
	default:
		return 0, 0, false
	}
}
func nativeAspect(ratio string) string {
	switch strings.ToLower(ratio) {
	case "square":
		return "1:1"
	case "landscape":
		return "16:9"
	case "portrait":
		return "9:16"
	default:
		return ratio
	}
}
