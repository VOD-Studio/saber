package platform

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"rua.plus/saber/internal/chat"
)

var publicMediaTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = publicDial
	return transport
}()

// DownloadMedia 下载接入端已经确认来源的媒体，限制大小并拒绝重定向和非公开地址。
// trustedBase 只允许运营者配置的 API 服务访问内网；平台凭据由调用方按固定域名绑定。
func DownloadMedia(request *http.Request, client *http.Client, trustedBase string, maxBytes int64, mime, name, kind string) (chat.Attachment, error) {
	if request.URL.User != nil || (request.URL.Scheme != "https" && request.URL.Scheme != "http") {
		return chat.Attachment{}, errors.New("无效平台媒体 URL")
	}
	base, err := url.Parse(trustedBase)
	if err != nil {
		return chat.Attachment{}, err
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("平台媒体不跟随重定向") }
	if base.Hostname() != request.URL.Hostname() {
		copyClient.Transport = publicMediaTransport
	}
	response, err := copyClient.Do(request)
	if err != nil {
		return chat.Attachment{}, errors.New("平台媒体网络请求失败")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return chat.Attachment{}, fmt.Errorf("平台媒体 HTTP %d", response.StatusCode)
	}
	if maxBytes <= 0 {
		maxBytes = 20 << 20
	}
	if response.ContentLength > maxBytes {
		return chat.Attachment{}, errors.New("平台媒体大小超限")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return chat.Attachment{}, err
	}
	if int64(len(data)) > maxBytes {
		return chat.Attachment{}, errors.New("平台媒体大小超限")
	}
	if mime == "" {
		mime = strings.Split(response.Header.Get("Content-Type"), ";")[0]
	}
	return mediaAttachment(data, mime, name, kind), nil
}

// UploadMedia 发送平台签发的上传请求；禁用重定向，外部上传主机只允许公开地址。
func UploadMedia(request *http.Request, client *http.Client, trustedBase string) error {
	base, err := url.Parse(trustedBase)
	if err != nil {
		return err
	}
	if request.URL.User != nil || (request.URL.Scheme != "https" && request.URL.Scheme != "http") {
		return errors.New("无效平台上传 URL")
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("平台上传不跟随重定向") }
	if base.Hostname() != request.URL.Hostname() {
		copyClient.Transport = publicMediaTransport
	}
	response, err := copyClient.Do(request)
	if err != nil {
		return errors.New("平台媒体上传网络请求失败")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("平台媒体上传 HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return err
}
