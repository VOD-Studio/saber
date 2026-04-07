package persona

import (
	"context"
	"errors"
	"strings"
	"testing"

	"maunium.net/go/mautrix/id"
)

// mockSender 实现 Sender 接口用于测试。
type mockSender struct {
	lastHTML  string
	lastPlain string
	lastError error
}

func (m *mockSender) SendFormattedText(_ context.Context, _ id.RoomID, html, plain string) error {
	m.lastHTML = html
	m.lastPlain = plain
	return m.lastError
}

// mockPersonaService 实现 PersonaService 接口用于测试。
type mockPersonaService struct {
	personas     map[string]*Persona
	roomPersonas map[id.RoomID]string
	createError  error
	deleteError  error
	setError     error
	clearError   error
}

func newMockService() *mockPersonaService {
	return &mockPersonaService{
		personas: map[string]*Persona{
			"catgirl": {ID: "catgirl", Name: "猫娘", Prompt: "喵", Description: "可爱", IsBuiltin: true},
			"butler":  {ID: "butler", Name: "管家", Prompt: "主人", Description: "优雅", IsBuiltin: true},
			"custom":  {ID: "custom", Name: "自定义", Prompt: "自定义提示", Description: "自定义描述", IsBuiltin: false},
		},
		roomPersonas: make(map[id.RoomID]string),
	}
}

func (m *mockPersonaService) List() []*Persona {
	result := make([]*Persona, 0, len(m.personas))
	for _, p := range m.personas {
		result = append(result, p)
	}
	return result
}

func (m *mockPersonaService) Get(id string) *Persona {
	return m.personas[id]
}

func (m *mockPersonaService) Create(id, name, prompt, description string) error {
	if m.createError != nil {
		return m.createError
	}
	if _, exists := m.personas[id]; exists {
		return errors.New("已存在")
	}
	m.personas[id] = &Persona{
		ID:          id,
		Name:        name,
		Prompt:      prompt,
		Description: description,
		IsBuiltin:   false,
	}
	return nil
}

func (m *mockPersonaService) Delete(id string) error {
	if m.deleteError != nil {
		return m.deleteError
	}
	p, exists := m.personas[id]
	if !exists {
		return nil
	}
	if p.IsBuiltin {
		return errors.New("内置人格不可删除")
	}
	delete(m.personas, id)
	// 清除使用此人格的房间映射
	for roomID, personaID := range m.roomPersonas {
		if personaID == id {
			delete(m.roomPersonas, roomID)
		}
	}
	return nil
}

func (m *mockPersonaService) GetRoomPersona(roomID id.RoomID) *Persona {
	personaID, exists := m.roomPersonas[roomID]
	if !exists {
		return nil
	}
	return m.personas[personaID]
}

func (m *mockPersonaService) SetRoomPersona(_ context.Context, roomID id.RoomID, personaID string) error {
	if m.setError != nil {
		return m.setError
	}
	if _, exists := m.personas[personaID]; !exists {
		return errors.New("人格不存在")
	}
	m.roomPersonas[roomID] = personaID
	return nil
}

func (m *mockPersonaService) ClearRoomPersona(_ context.Context, roomID id.RoomID) error {
	if m.clearError != nil {
		return m.clearError
	}
	delete(m.roomPersonas, roomID)
	return nil
}

func TestPersonaCommand_Handle_NoArgs(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "人格管理命令") {
		t.Error("应显示帮助信息")
	}
}

func TestPersonaCommand_Handle_Help(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{"help"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "!persona list") {
		t.Error("帮助信息应包含 list 命令")
	}
}

func TestPersonaCommand_Handle_List(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{"list"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "猫娘") || !strings.Contains(sender.lastPlain, "管家") {
		t.Error("列表应包含内置人格")
	}
	if !strings.Contains(sender.lastHTML, "<table>") {
		t.Error("应返回 HTML 表格")
	}
}

func TestPersonaCommand_Handle_Set(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)
	roomID := id.RoomID("!test:example.com")

	err := cmd.Handle(context.Background(), "user", roomID, []string{"set", "catgirl"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "已将当前房间的人格设置为") {
		t.Errorf("应确认设置成功，实际: %s", sender.lastPlain)
	}

	// 验证服务状态
	p := service.GetRoomPersona(roomID)
	if p == nil || p.ID != "catgirl" {
		t.Error("房间人格未正确设置")
	}
}

func TestPersonaCommand_Handle_SetNonexistent(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{"set", "nonexistent"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "不存在") {
		t.Errorf("应提示人格不存在，实际: %s", sender.lastPlain)
	}
}

func TestPersonaCommand_Handle_Clear(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)
	roomID := id.RoomID("!test:example.com")

	// 先设置
	_ = service.SetRoomPersona(context.Background(), roomID, "catgirl")

	// 再清除
	err := cmd.Handle(context.Background(), "user", roomID, []string{"clear"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "已清除") {
		t.Errorf("应确认清除成功，实际: %s", sender.lastPlain)
	}

	if service.GetRoomPersona(roomID) != nil {
		t.Error("房间人格应被清除")
	}
}

func TestPersonaCommand_Handle_Status(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)
	roomID := id.RoomID("!test:example.com")

	// 未设置人格
	err := cmd.Handle(context.Background(), "user", roomID, []string{"status"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !strings.Contains(sender.lastPlain, "未设置人格") {
		t.Errorf("应显示未设置，实际: %s", sender.lastPlain)
	}

	// 设置人格后
	_ = service.SetRoomPersona(context.Background(), roomID, "catgirl")
	err = cmd.Handle(context.Background(), "user", roomID, []string{"status"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !strings.Contains(sender.lastPlain, "猫娘") {
		t.Errorf("应显示当前人格，实际: %s", sender.lastPlain)
	}
}

func TestPersonaCommand_Handle_New(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	args := []string{"new", "test", "测试人格", "测试提示词", "测试描述"}
	err := cmd.Handle(context.Background(), "user", "room", args)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "已创建人格") {
		t.Errorf("应确认创建成功，实际: %s", sender.lastPlain)
	}

	p := service.Get("test")
	if p == nil {
		t.Fatal("人格未创建")
	}
	if p.Name != "测试人格" {
		t.Errorf("Name = %q, want %q", p.Name, "测试人格")
	}
}

func TestPersonaCommand_Handle_NewNotEnoughArgs(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{"new", "test"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "用法") {
		t.Errorf("应显示用法提示，实际: %s", sender.lastPlain)
	}
}

func TestPersonaCommand_Handle_Delete(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{"del", "custom"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "已删除人格") {
		t.Errorf("应确认删除成功，实际: %s", sender.lastPlain)
	}

	if service.Get("custom") != nil {
		t.Error("人格应被删除")
	}
}

func TestPersonaCommand_Handle_DeleteBuiltin(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{"del", "catgirl"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "不可删除") {
		t.Errorf("应提示内置人格不可删除，实际: %s", sender.lastPlain)
	}
}

func TestPersonaCommand_Handle_DeleteNonexistent(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{"del", "nonexistent"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "不存在") {
		t.Errorf("应提示人格不存在，实际: %s", sender.lastPlain)
	}
}

func TestPersonaCommand_Handle_UnknownSubcommand(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)

	err := cmd.Handle(context.Background(), "user", "room", []string{"unknown"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !strings.Contains(sender.lastPlain, "未知子命令") {
		t.Errorf("应提示未知子命令，实际: %s", sender.lastPlain)
	}
}

func TestPersonaCommand_EscapeHTML(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal", "normal"},
		{"<script>", "&lt;script&gt;"},
		{`"quoted"`, "&quot;quoted&quot;"},
		{"a&b", "a&amp;b"},
		{"'single'", "&#39;single&#39;"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeHTML(tt.input)
			if result != tt.expected {
				t.Errorf("escapeHTML(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestPersonaCommand_ListWithActivePersona(t *testing.T) {
	sender := &mockSender{}
	service := newMockService()
	cmd := NewPersonaCommand(sender, service)
	roomID := id.RoomID("!test:example.com")

	// 设置房间人格
	_ = service.SetRoomPersona(context.Background(), roomID, "catgirl")

	err := cmd.Handle(context.Background(), "user", roomID, []string{"list"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	// 检查当前激活的人格被标记
	if !strings.Contains(sender.lastHTML, "当前") {
		t.Error("应标记当前激活的人格")
	}
	if !strings.Contains(sender.lastPlain, "[当前]") {
		t.Error("纯文本应标记当前激活的人格")
	}
}
