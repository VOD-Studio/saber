package matrix

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
)

func TestMediaInfo(t *testing.T) {
	tests := []struct {
		name     string
		info     *MediaInfo
		wantType string
	}{
		{
			name: "image type",
			info: &MediaInfo{
				Type:     "image",
				URL:      "mxc://example.com/image123",
				Body:     "测试图片",
				MimeType: "image/png",
			},
			wantType: "image",
		},
		{
			name: "video type",
			info: &MediaInfo{
				Type:     "video",
				URL:      "mxc://example.com/video123",
				Body:     "测试视频",
				MimeType: "video/mp4",
			},
			wantType: "video",
		},
		{
			name: "audio type",
			info: &MediaInfo{
				Type:     "audio",
				URL:      "mxc://example.com/audio123",
				Body:     "测试音频",
				MimeType: "audio/ogg",
			},
			wantType: "audio",
		},
		{
			name: "file type",
			info: &MediaInfo{
				Type:     "file",
				URL:      "mxc://example.com/file123",
				Body:     "测试文件",
				MimeType: "application/pdf",
			},
			wantType: "file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.info.Type != tt.wantType {
				t.Errorf("MediaInfo.Type = %v, want %v", tt.info.Type, tt.wantType)
			}
			if tt.info.URL == "" {
				t.Error("MediaInfo.URL should not be empty")
			}
		})
	}
}

func TestExtractMediaInfo(t *testing.T) {
	tests := []struct {
		name    string
		content *event.MessageEventContent
		want    *MediaInfo
	}{
		{
			name: "image message",
			content: &event.MessageEventContent{
				MsgType: event.MsgImage,
				Body:    "这是一张图片",
				URL:     "mxc://example.com/img123",
				Info: &event.FileInfo{
					MimeType: "image/jpeg",
					Size:     102400,
				},
			},
			want: &MediaInfo{
				Type:     "image",
				URL:      "mxc://example.com/img123",
				Body:     "这是一张图片",
				MimeType: "image/jpeg",
			},
		},
		{
			name: "video message",
			content: &event.MessageEventContent{
				MsgType: event.MsgVideo,
				Body:    "这是一个视频",
				URL:     "mxc://example.com/vid123",
				Info: &event.FileInfo{
					MimeType: "video/mp4",
					Size:     5242880,
					Duration: 120000,
					Width:    1920,
					Height:   1080,
				},
			},
			want: &MediaInfo{
				Type:     "video",
				URL:      "mxc://example.com/vid123",
				Body:     "这是一个视频",
				MimeType: "video/mp4",
			},
		},
		{
			name: "audio message",
			content: &event.MessageEventContent{
				MsgType: event.MsgAudio,
				Body:    "这是一段音频",
				URL:     "mxc://example.com/aud123",
				Info: &event.FileInfo{
					MimeType: "audio/ogg",
					Size:     1048576,
					Duration: 180000,
				},
			},
			want: &MediaInfo{
				Type:     "audio",
				URL:      "mxc://example.com/aud123",
				Body:     "这是一段音频",
				MimeType: "audio/ogg",
			},
		},
		{
			name: "file message",
			content: &event.MessageEventContent{
				MsgType: event.MsgFile,
				Body:    "这是一个文件",
				URL:     "mxc://example.com/doc123",
				Info: &event.FileInfo{
					MimeType: "application/pdf",
					Size:     2097152,
				},
			},
			want: &MediaInfo{
				Type:     "file",
				URL:      "mxc://example.com/doc123",
				Body:     "这是一个文件",
				MimeType: "application/pdf",
			},
		},
		{
			name: "encrypted file",
			content: &event.MessageEventContent{
				MsgType: event.MsgImage,
				Body:    "加密图片",
				File: &event.EncryptedFileInfo{
					URL: "mxc://example.com/enc123",
				},
				Info: &event.FileInfo{
					MimeType: "image/png",
				},
			},
			want: &MediaInfo{
				Type: "image",
				File: &event.EncryptedFileInfo{
					URL: "mxc://example.com/enc123",
				},
				Body:     "加密图片",
				MimeType: "image/png",
			},
		},
		{
			name: "text message (not media)",
			content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "这是文本消息",
			},
			want: nil,
		},
		{
			name:    "nil content",
			content: nil,
			want:    nil,
		},
		{
			name: "missing mime type",
			content: &event.MessageEventContent{
				MsgType: event.MsgImage,
				Body:    "无 MIME 类型",
				URL:     "mxc://example.com/img456",
			},
			want: &MediaInfo{
				Type:     "image",
				URL:      "mxc://example.com/img456",
				Body:     "无 MIME 类型",
				MimeType: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractMediaInfo(tt.content)
			if got == nil && tt.want == nil {
				return
			}
			if got == nil && tt.want != nil {
				t.Fatalf("ExtractMediaInfo() = nil, want %v", tt.want)
			}
			if got != nil && tt.want == nil {
				t.Fatalf("ExtractMediaInfo() = %v, want nil", got)
			}

			if got.Type != tt.want.Type {
				t.Errorf("Type = %v, want %v", got.Type, tt.want.Type)
			}
			if got.URL != tt.want.URL {
				t.Errorf("URL = %v, want %v", got.URL, tt.want.URL)
			}
			if got.Body != tt.want.Body {
				t.Errorf("Body = %v, want %v", got.Body, tt.want.Body)
			}
			if got.MimeType != tt.want.MimeType {
				t.Errorf("MimeType = %v, want %v", got.MimeType, tt.want.MimeType)
			}

			if tt.want.File != nil {
				if got.File == nil {
					t.Error("File = nil, want non-nil")
				} else if got.File.URL != tt.want.File.URL {
					t.Errorf("File.URL = %v, want %v", got.File.URL, tt.want.File.URL)
				}
			}
		})
	}
}

func TestMediaService(t *testing.T) {
	client := &mautrix.Client{}

	tests := []struct {
		name    string
		maxSize int64
	}{
		{"no size limit", 0},
		{"1MB limit", 1048576},
		{"10MB limit", 10485760},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewMediaService(client, tt.maxSize)
			if service == nil {
				t.Fatal("NewMediaService() returned nil")
			}
			if service.client != client {
				t.Error("client not set correctly")
			}
			if service.maxSize != tt.maxSize {
				t.Errorf("maxSize = %v, want %v", service.maxSize, tt.maxSize)
			}
		})
	}
}

func TestEncodeImageAsDataURL(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		mimeType   string
		wantPrefix string
	}{
		{
			name:       "JPEG with explicit mime type",
			data:       []byte{0xFF, 0xD8, 0xFF, 0xE0},
			mimeType:   "image/jpeg",
			wantPrefix: "data:image/jpeg;base64,",
		},
		{
			name:       "PNG with explicit mime type",
			data:       []byte{0x89, 0x50, 0x4E, 0x47},
			mimeType:   "image/png",
			wantPrefix: "data:image/png;base64,",
		},
		{
			name:       "default to JPEG when mime type empty",
			data:       []byte{0xFF, 0xD8, 0xFF},
			mimeType:   "",
			wantPrefix: "data:image/jpeg;base64,",
		},
		{
			name:       "empty data",
			data:       []byte{},
			mimeType:   "image/jpeg",
			wantPrefix: "data:image/jpeg;base64,",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := encodeImageAsDataURL(tt.data, tt.mimeType)

			if !strings.HasPrefix(result, tt.wantPrefix) {
				t.Errorf("encodeImageAsDataURL() prefix = %v, want %v", result[:len(tt.wantPrefix)], tt.wantPrefix)
			}

			encodedPart := strings.TrimPrefix(result, tt.wantPrefix)
			var expectedEncoded string
			switch tt.name {
			case "JPEG with explicit mime type":
				expectedEncoded = "/9j/4A=="
			case "PNG with explicit mime type":
				expectedEncoded = "iVBORw=="
			case "default to JPEG when mime type empty":
				expectedEncoded = "/9j/"
			case "empty data":
				expectedEncoded = ""
			}

			if encodedPart != expectedEncoded {
				t.Errorf("encodeImageAsDataURL() encoded = %v, want %v", encodedPart, expectedEncoded)
			}
		})
	}
}
