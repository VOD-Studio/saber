//go:build goolm

package matrix

import (
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TestMediaInfo_GetMXCURI 测试 GetMXCURI 方法。
func TestMediaInfo_GetMXCURI(t *testing.T) {
	tests := []struct {
		name          string
		info          *MediaInfo
		wantURI       string
		wantEncrypted bool
	}{
		{
			name: "未加密图片 URL",
			info: &MediaInfo{
				URL: "mxc://example.com/abc123",
			},
			wantURI:       "mxc://example.com/abc123",
			wantEncrypted: false,
		},
		{
			name: "加密图片 File URL",
			info: &MediaInfo{
				URL: "mxc://example.com/unencrypted",
				File: &event.EncryptedFileInfo{
					URL: "mxc://example.com/encrypted",
				},
			},
			wantURI:       "mxc://example.com/encrypted",
			wantEncrypted: true,
		},
		{
			name: "空 URL",
			info: &MediaInfo{
				URL: "",
			},
			wantURI:       "",
			wantEncrypted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri, encrypted := tt.info.GetMXCURI()
			if string(uri) != tt.wantURI {
				t.Errorf("GetMXCURI() uri = %v, want %v", uri, tt.wantURI)
			}
			if encrypted != tt.wantEncrypted {
				t.Errorf("GetMXCURI() encrypted = %v, want %v", encrypted, tt.wantEncrypted)
			}
		})
	}
}

// TestMediaInfo_GetMimeType 测试 GetMimeType 方法。
func TestMediaInfo_GetMimeType(t *testing.T) {
	tests := []struct {
		name     string
		info     *MediaInfo
		wantType string
	}{
		{
			name: "直接设置 MimeType",
			info: &MediaInfo{
				MimeType: "image/png",
			},
			wantType: "image/png",
		},
		{
			name: "从 Info 获取 MimeType",
			info: &MediaInfo{
				Info: &event.FileInfo{
					MimeType: "video/mp4",
				},
			},
			wantType: "video/mp4",
		},
		{
			name: "默认图片类型",
			info: &MediaInfo{
				Type: "image",
			},
			wantType: "image/jpeg",
		},
		{
			name: "默认视频类型",
			info: &MediaInfo{
				Type: "video",
			},
			wantType: "video/mp4",
		},
		{
			name: "默认音频类型",
			info: &MediaInfo{
				Type: "audio",
			},
			wantType: "audio/mpeg",
		},
		{
			name:     "未知类型默认",
			info:     &MediaInfo{Type: "unknown"},
			wantType: "application/octet-stream",
		},
		{
			name:     "MimeType 优先于 Info",
			info:     &MediaInfo{MimeType: "image/gif", Info: &event.FileInfo{MimeType: "image/png"}},
			wantType: "image/gif",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.GetMimeType()
			if got != tt.wantType {
				t.Errorf("GetMimeType() = %v, want %v", got, tt.wantType)
			}
		})
	}
}

// TestMediaInfo_GetSize 测试 GetSize 方法。
func TestMediaInfo_GetSize(t *testing.T) {
	tests := []struct {
		name     string
		info     *MediaInfo
		wantSize int
	}{
		{
			name: "有 Info 设置 Size",
			info: &MediaInfo{
				Info: &event.FileInfo{
					Size: 1024,
				},
			},
			wantSize: 1024,
		},
		{
			name:     "无 Info 返回 0",
			info:     &MediaInfo{},
			wantSize: 0,
		},
		{
			name: "Info 为 nil 返回 0",
			info: &MediaInfo{
				Info: nil,
			},
			wantSize: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.GetSize()
			if got != tt.wantSize {
				t.Errorf("GetSize() = %v, want %v", got, tt.wantSize)
			}
		})
	}
}

// TestMediaInfo_ValidateSize 测试 ValidateSize 方法。
func TestMediaInfo_ValidateSize(t *testing.T) {
	tests := []struct {
		name    string
		info    *MediaInfo
		maxSize int64
		wantErr bool
	}{
		{
			name: "无限制",
			info: &MediaInfo{
				Info: &event.FileInfo{Size: 10000},
			},
			maxSize: 0,
			wantErr: false,
		},
		{
			name: "大小在限制内",
			info: &MediaInfo{
				Info: &event.FileInfo{Size: 500},
			},
			maxSize: 1000,
			wantErr: false,
		},
		{
			name: "超过限制",
			info: &MediaInfo{
				Info: &event.FileInfo{Size: 2000},
			},
			maxSize: 1000,
			wantErr: true,
		},
		{
			name:    "未知大小不报错",
			info:    &MediaInfo{},
			maxSize: 1000,
			wantErr: false,
		},
		{
			name: "负数限制视为无限制",
			info: &MediaInfo{
				Info: &event.FileInfo{Size: 10000},
			},
			maxSize: -1,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.info.ValidateSize(tt.maxSize)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSize() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestMediaInfo_TypeDefaults 测试不同 Type 的默认 MimeType。
func TestMediaInfo_TypeDefaults(t *testing.T) {
	types := []struct {
		mediaType string
		expected  string
	}{
		{"image", "image/jpeg"},
		{"video", "video/mp4"},
		{"audio", "audio/mpeg"},
		{"document", "application/octet-stream"},
		{"", "application/octet-stream"},
	}

	for _, tc := range types {
		t.Run(tc.mediaType, func(t *testing.T) {
			info := &MediaInfo{Type: tc.mediaType}
			got := info.GetMimeType()
			if got != tc.expected {
				t.Errorf("Type %q: GetMimeType() = %v, want %v", tc.mediaType, got, tc.expected)
			}
		})
	}
}

// TestEncryptedFileInfo 测试 EncryptedFileInfo 结构。
func TestEncryptedFileInfo(t *testing.T) {
	file := &event.EncryptedFileInfo{
		URL: id.ContentURIString("mxc://example.com/encrypted"),
	}

	if file.URL != "mxc://example.com/encrypted" {
		t.Errorf("EncryptedFileInfo.URL = %v", file.URL)
	}
}

// TestFileInfo 测试 FileInfo 结构。
func TestFileInfo(t *testing.T) {
	info := &event.FileInfo{
		MimeType: "image/png",
		Size:     2048,
		Width:    100,
		Height:   200,
	}

	if info.MimeType != "image/png" {
		t.Errorf("FileInfo.MimeType = %v", info.MimeType)
	}
	if info.Size != 2048 {
		t.Errorf("FileInfo.Size = %v", info.Size)
	}
	if info.Width != 100 || info.Height != 200 {
		t.Errorf("FileInfo dimensions = %vx%v", info.Width, info.Height)
	}
}
