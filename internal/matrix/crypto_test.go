//go:build goolm

// Package matrix_test 包含端到端加密服务的单元测试。
package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TestNoopCryptoService 测试 NoopCryptoService 的所有方法。
//
// 该测试覆盖以下场景：
//   - Init 方法始终返回 nil
//   - Decrypt 方法返回原始事件不变
//   - IsEnabled 方法始终返回 false
//   - 零值 NoopCryptoService 是有效的
func TestNoopCryptoService(t *testing.T) {
	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "Init 始终返回 nil",
			test: func(t *testing.T) {
				svc := NewNoopCryptoService()
				ctx := context.Background()

				err := svc.Init(ctx)
				if err != nil {
					t.Errorf("期望 Init 返回 nil，实际返回: %v", err)
				}
			},
		},
		{
			name: "Decrypt 返回原始事件",
			test: func(t *testing.T) {
				svc := NewNoopCryptoService()
				ctx := context.Background()

				originalEvent := &event.Event{
					Type:   event.EventMessage,
					RoomID: id.RoomID("!test:example.com"),
					Sender: id.UserID("@alice:example.com"),
					Content: event.Content{
						Parsed: &event.MessageEventContent{
							MsgType: event.MsgText,
							Body:    "Hello, World!",
						},
					},
				}

				result, err := svc.Decrypt(ctx, originalEvent)
				if err != nil {
					t.Errorf("期望 Decrypt 返回 nil 错误，实际返回: %v", err)
				}
				if result != originalEvent {
					t.Errorf("期望 Decrypt 返回原始事件，实际返回不同的事件")
				}
			},
		},
		{
			name: "Decrypt 处理 nil 事件",
			test: func(t *testing.T) {
				svc := NewNoopCryptoService()
				ctx := context.Background()

				result, err := svc.Decrypt(ctx, nil)
				if err != nil {
					t.Errorf("期望 Decrypt 返回 nil 错误，实际返回: %v", err)
				}
				if result != nil {
					t.Errorf("期望 Decrypt 返回 nil，实际返回: %v", result)
				}
			},
		},
		{
			name: "Decrypt 处理加密事件类型",
			test: func(t *testing.T) {
				svc := NewNoopCryptoService()
				ctx := context.Background()

				encryptedEvent := &event.Event{
					Type:   event.EventEncrypted,
					RoomID: id.RoomID("!test:example.com"),
					Sender: id.UserID("@alice:example.com"),
					Content: event.Content{
						Raw: map[string]any{
							"algorithm": "m.megolm.v1.aes-sha2",
						},
					},
				}

				result, err := svc.Decrypt(ctx, encryptedEvent)
				if err != nil {
					t.Errorf("期望 Decrypt 返回 nil 错误，实际返回: %v", err)
				}
				if result != encryptedEvent {
					t.Errorf("期望 Decrypt 返回原始加密事件，实际返回不同的事件")
				}
			},
		},
		{
			name: "IsEnabled 始终返回 false",
			test: func(t *testing.T) {
				svc := NewNoopCryptoService()

				if svc.IsEnabled() {
					t.Errorf("期望 IsEnabled 返回 false，实际返回 true")
				}
			},
		},
		{
			name: "零值 NoopCryptoService 有效",
			test: func(t *testing.T) {
				var svc NoopCryptoService // 零值
				ctx := context.Background()

				// Init 应该工作
				err := svc.Init(ctx)
				if err != nil {
					t.Errorf("期望 Init 返回 nil，实际返回: %v", err)
				}

				// IsEnabled 应该返回 false
				if svc.IsEnabled() {
					t.Errorf("期望 IsEnabled 返回 false，实际返回 true")
				}

				// Decrypt 应该工作
				evt := &event.Event{Type: event.EventMessage}
				result, err := svc.Decrypt(ctx, evt)
				if err != nil {
					t.Errorf("期望 Decrypt 返回 nil 错误，实际返回: %v", err)
				}
				if result != evt {
					t.Errorf("期望 Decrypt 返回原始事件")
				}
			},
		},
		{
			name: "多次调用 Init 无副作用",
			test: func(t *testing.T) {
				svc := NewNoopCryptoService()
				ctx := context.Background()

				for i := range 5 {
					err := svc.Init(ctx)
					if err != nil {
						t.Errorf("第 %d 次 Init 失败: %v", i+1, err)
					}
				}
			},
		},
		{
			name: "取消上下文不影响 Init",
			test: func(t *testing.T) {
				svc := NewNoopCryptoService()
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // 立即取消

				err := svc.Init(ctx)
				if err != nil {
					t.Errorf("NoopCryptoService.Init 不应受取消上下文影响，实际返回: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.test)
	}
}

// TestGeneratePickleKey 测试 GeneratePickleKey 函数。
//
// 该测试覆盖以下场景：
//   - 返回 32 字节密钥
//   - 每次调用生成不同的密钥
//   - 密钥包含随机数据（非全零）
func TestGeneratePickleKey(t *testing.T) {
	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "返回 32 字节密钥",
			test: func(t *testing.T) {
				key, err := GeneratePickleKey()
				if err != nil {
					t.Fatalf("期望 GeneratePickleKey 成功，实际返回错误: %v", err)
				}
				if len(key) != 32 {
					t.Errorf("期望密钥长度为 32，实际长度: %d", len(key))
				}
			},
		},
		{
			name: "多次调用返回不同密钥",
			test: func(t *testing.T) {
				key1, err := GeneratePickleKey()
				if err != nil {
					t.Fatalf("第一次调用失败: %v", err)
				}

				key2, err := GeneratePickleKey()
				if err != nil {
					t.Fatalf("第二次调用失败: %v", err)
				}

				// 比较两个密钥
				if string(key1) == string(key2) {
					t.Errorf("期望生成不同的密钥，但两次调用返回了相同的密钥")
				}
			},
		},
		{
			name: "密钥非全零",
			test: func(t *testing.T) {
				key, err := GeneratePickleKey()
				if err != nil {
					t.Fatalf("期望 GeneratePickleKey 成功，实际返回错误: %v", err)
				}

				// 检查是否全零
				allZero := true
				for _, b := range key {
					if b != 0 {
						allZero = false
						break
					}
				}
				if allZero {
					t.Errorf("密钥不应全为零")
				}
			},
		},
		{
			name: "密钥具有足够的熵",
			test: func(t *testing.T) {
				// 生成多个密钥并检查它们有足够的差异
				keys := make([][]byte, 10)
				for i := range 10 {
					key, err := GeneratePickleKey()
					if err != nil {
						t.Fatalf("第 %d 次调用失败: %v", i+1, err)
					}
					keys[i] = key
				}

				// 检查每个密钥与其他密钥的差异
				for i := range 10 {
					for j := i + 1; j < 10; j++ {
						diff := 0
						for k := range 32 {
							if keys[i][k] != keys[j][k] {
								diff++
							}
						}
						// 期望至少一半字节不同（概率极高）
						if diff < 16 {
							t.Errorf("密钥 %d 和 %d 差异过小（仅 %d/32 字节不同）", i, j, diff)
						}
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.test)
	}
}

// TestGeneratePickleKeyEdgeCases 测试 GeneratePickleKey 的边缘情况。
//
// 该测试覆盖以下场景：
//   - NewOlmCryptoService 创建有效的实例
//   - 创建后 cryptoHelper 字段为 nil
//   - 创建后 IsEnabled 返回 false（因为未初始化）
func TestOlmCryptoServiceCreation(t *testing.T) {
	t.Run("NewOlmCryptoService 创建有效实例", func(t *testing.T) {
		// 注意：这里不使用真实的客户端，因为只需要测试结构体创建
		svc := NewOlmCryptoService(nil, "/tmp/test.db", []byte("test-key-32-bytes-1234567890"))

		if svc == nil {
			t.Fatal("期望返回非 nil 实例")
		}
		if svc.sessionPath != "/tmp/test.db" {
			t.Errorf("期望 sessionPath 为 /tmp/test.db，实际: %s", svc.sessionPath)
		}
		if string(svc.pickleKey) != "test-key-32-bytes-1234567890" {
			t.Errorf("pickleKey 未正确设置")
		}
	})

	t.Run("创建后 IsEnabled 返回 false", func(t *testing.T) {
		svc := NewOlmCryptoService(nil, "/tmp/test.db", []byte("test-key"))
		if svc.IsEnabled() {
			t.Errorf("未初始化的 OlmCryptoService.IsEnabled 应返回 false")
		}
	})

	t.Run("创建后 cryptoHelper 为 nil", func(t *testing.T) {
		svc := NewOlmCryptoService(nil, "/tmp/test.db", []byte("test-key"))
		if svc.cryptoHelper != nil {
			t.Errorf("期望 cryptoHelper 为 nil")
		}
	})
}

// TestOlmCryptoServiceDecrypt 测试 OlmCryptoService.Decrypt 方法。
//
// 该测试覆盖以下场景：
//   - 未初始化时 Decrypt 返回错误
func TestOlmCryptoServiceDecrypt(t *testing.T) {
	t.Run("未初始化时 Decrypt 返回错误", func(t *testing.T) {
		svc := NewOlmCryptoService(nil, "/tmp/test.db", []byte("test-key"))
		ctx := context.Background()
		evt := &event.Event{
			Type:   event.EventEncrypted,
			RoomID: id.RoomID("!test:example.com"),
		}

		result, err := svc.Decrypt(ctx, evt)
		if err == nil {
			t.Errorf("期望返回错误，实际返回 nil")
		}
		if result != nil {
			t.Errorf("期望返回 nil 事件，实际返回: %v", result)
		}
		if err != nil && err.Error() != "crypto helper not initialized" {
			t.Errorf("期望错误消息 'crypto helper not initialized'，实际: %v", err)
		}
	})
}

// TestCryptoServiceInterface 测试 CryptoService 接口实现。
//
// 该测试确保 NoopCryptoService 和 OlmCryptoService 都正确实现了 CryptoService 接口。
func TestCryptoServiceInterface(t *testing.T) {
	t.Run("NoopCryptoService 实现 CryptoService 接口", func(t *testing.T) {
		var _ CryptoService = NewNoopCryptoService()
	})

	t.Run("OlmCryptoService 实现 CryptoService 接口", func(t *testing.T) {
		var _ CryptoService = NewOlmCryptoService(nil, "", nil)
	})
}

// TestNoopCryptoServiceConcurrency 测试 NoopCryptoService 的并发安全性。
//
// 虽然 NoopCryptoService 是无状态的，但仍应验证并发调用不会导致问题。
func TestNoopCryptoServiceConcurrency(t *testing.T) {
	svc := NewNoopCryptoService()
	ctx := context.Background()

	// 使用通道收集错误
	errChan := make(chan error, 100)

	// 并发调用
	for range 50 {
		go func() {
			evt := &event.Event{
				Type:   event.EventMessage,
				RoomID: id.RoomID("!test:example.com"),
			}
			_, err := svc.Decrypt(ctx, evt)
			errChan <- err
		}()

		go func() {
			errChan <- svc.Init(ctx)
		}()

		go func() {
			_ = svc.IsEnabled()
			errChan <- nil
		}()
	}

	// 收集结果
	for range 150 {
		if err := <-errChan; err != nil {
			t.Errorf("并发调用失败: %v", err)
		}
	}
}

// TestGeneratePickleKeyConcurrent 测试 GeneratePickleKey 的并发安全性。
func TestGeneratePickleKeyConcurrent(t *testing.T) {
	const goroutines = 20
	keys := make([][]byte, goroutines)
	errChan := make(chan error, goroutines)

	for i := range goroutines {
		go func(idx int) {
			key, err := GeneratePickleKey()
			if err != nil {
				errChan <- err
				return
			}
			keys[idx] = key
			errChan <- nil
		}(i)
	}

	// 等待所有 goroutine 完成
	for range goroutines {
		if err := <-errChan; err != nil {
			t.Errorf("并发生成密钥失败: %v", err)
		}
	}

	// 验证所有密钥都不同
	for i := range goroutines {
		for j := i + 1; j < goroutines; j++ {
			if string(keys[i]) == string(keys[j]) {
				t.Errorf("并发生成的密钥 %d 和 %d 相同", i, j)
			}
		}
	}
}

// TestNoopCryptoServiceContextHandling 测试 NoopCryptoService 的上下文处理。
func TestNoopCryptoServiceContextHandling(t *testing.T) {
	svc := NewNoopCryptoService()

	t.Run("超时上下文", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()

		// 即使上下文已过期，NoopCryptoService 也应该工作
		err := svc.Init(ctx)
		if err != nil {
			t.Errorf("NoopCryptoService.Init 不应受超时上下文影响")
		}
	})

	t.Run("取消上下文", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := svc.Init(ctx)
		if err != nil {
			t.Errorf("NoopCryptoService.Init 不应受取消上下文影响")
		}
	})
}

// TestGeneratePickleKeyEdgeCases 测试 GeneratePickleKey 的边缘情况。
func TestGeneratePickleKeyEdgeCases(t *testing.T) {
	t.Run("生成的密钥长度正确", func(t *testing.T) {
		for i := range 10 {
			key, err := GeneratePickleKey()
			if err != nil {
				t.Fatalf("调用 %d 失败: %v", i, err)
			}
			if len(key) != 32 {
				t.Errorf("调用 %d: 期望密钥长度 32，实际 %d", i, len(key))
			}
		}
	})

	t.Run("密钥可以安全复制", func(t *testing.T) {
		original, err := GeneratePickleKey()
		if err != nil {
			t.Fatalf("生成密钥失败: %v", err)
		}

		// 复制密钥
		copyKey := make([]byte, len(original))
		copy(copyKey, original)

		// 修改副本不应影响原始
		copyKey[0] = ^copyKey[0]
		if original[0] == copyKey[0] {
			t.Errorf("修改副本不应影响原始密钥")
		}
	})
}

// TestOlmCryptoServiceFields 测试 OlmCryptoService 字段访问。
func TestOlmCryptoServiceFields(t *testing.T) {
	t.Run("字段正确设置", func(t *testing.T) {
		client := (*mautrix.Client)(nil) // 使用 nil 客户端进行结构测试
		sessionPath := "/path/to/session.db"
		pickleKey := []byte("32-byte-pickle-key-for-testing!")

		svc := NewOlmCryptoService(client, sessionPath, pickleKey)

		if svc.client != client {
			t.Errorf("客户端未正确设置")
		}
		if svc.sessionPath != sessionPath {
			t.Errorf("期望 sessionPath 为 %s，实际 %s", sessionPath, svc.sessionPath)
		}
		if string(svc.pickleKey) != string(pickleKey) {
			t.Errorf("pickleKey 未正确设置")
		}
	})

	t.Run("nil 参数处理", func(t *testing.T) {
		// 应该可以创建，Init 时会失败
		svc := NewOlmCryptoService(nil, "", nil)
		if svc == nil {
			t.Errorf("应该能够创建带有 nil 参数的 OlmCryptoService")
		}
	})
}
