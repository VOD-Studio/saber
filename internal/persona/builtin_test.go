package persona

import (
	"testing"
	"time"
)

func TestPersonaIsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		persona  *Persona
		expected bool
	}{
		{
			name:     "nil persona",
			persona:  nil,
			expected: true,
		},
		{
			name:     "empty ID",
			persona:  &Persona{ID: ""},
			expected: true,
		},
		{
			name:     "valid persona",
			persona:  &Persona{ID: "test"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.persona.IsEmpty(); got != tt.expected {
				t.Errorf("Persona.IsEmpty() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestPersonaDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		persona  *Persona
		expected string
	}{
		{
			name:     "nil persona",
			persona:  nil,
			expected: "",
		},
		{
			name:     "empty persona",
			persona:  &Persona{},
			expected: "",
		},
		{
			name:     "valid persona",
			persona:  &Persona{ID: "test", Name: "测试人格"},
			expected: "测试人格",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.persona.DisplayName(); got != tt.expected {
				t.Errorf("Persona.DisplayName() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestPersonaFullPrompt(t *testing.T) {
	tests := []struct {
		name       string
		persona    *Persona
		basePrompt string
		expected   string
	}{
		{
			name:       "nil persona",
			persona:    nil,
			basePrompt: "base",
			expected:   "base",
		},
		{
			name:       "empty persona",
			persona:    &Persona{},
			basePrompt: "base",
			expected:   "base",
		},
		{
			name:       "empty base prompt",
			persona:    &Persona{ID: "test", Prompt: "persona prompt"},
			basePrompt: "",
			expected:   "persona prompt",
		},
		{
			name:       "both prompts",
			persona:    &Persona{ID: "test", Prompt: "persona prompt"},
			basePrompt: "base prompt",
			expected:   "base prompt\n\n---\n\npersona prompt",
		},
		{
			name:       "empty persona prompt",
			persona:    &Persona{ID: "test", Prompt: ""},
			basePrompt: "base prompt",
			expected:   "base prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.persona.FullPrompt(tt.basePrompt); got != tt.expected {
				t.Errorf("Persona.FullPrompt() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestBuiltinPersonasCount(t *testing.T) {
	// 确保至少有5个内置人格
	if len(BuiltinPersonas) < 5 {
		t.Errorf("Expected at least 5 builtin personas, got %d", len(BuiltinPersonas))
	}
}

func TestBuiltinPersonasFields(t *testing.T) {
	// 验证每个内置人格都有必要的字段
	for i, p := range BuiltinPersonas {
		if p.ID == "" {
			t.Errorf("BuiltinPersonas[%d].ID is empty", i)
		}
		if p.Name == "" {
			t.Errorf("BuiltinPersonas[%d].Name is empty", i)
		}
		if p.Prompt == "" {
			t.Errorf("BuiltinPersonas[%d].Prompt is empty", i)
		}
		if p.Description == "" {
			t.Errorf("BuiltinPersonas[%d].Description is empty", i)
		}
		if !p.IsBuiltin {
			t.Errorf("BuiltinPersonas[%d].IsBuiltin should be true", i)
		}
	}
}

func TestGetBuiltinPersona(t *testing.T) {
	tests := []struct {
		id       string
		expected bool
	}{
		{"catgirl", true},
		{"butler", true},
		{"pirate", true},
		{"tsundere", true},
		{"poet", true},
		{"nonexistent", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			p := GetBuiltinPersona(tt.id)
			if (p != nil) != tt.expected {
				t.Errorf("GetBuiltinPersona(%q) = %v, want exists: %v", tt.id, p, tt.expected)
			}
		})
	}
}

func TestBuiltinPersonaIDs(t *testing.T) {
	ids := BuiltinPersonaIDs()
	if len(ids) != len(BuiltinPersonas) {
		t.Errorf("BuiltinPersonaIDs() returned %d IDs, want %d", len(ids), len(BuiltinPersonas))
	}

	// 验证所有ID都在列表中
	for _, p := range BuiltinPersonas {
		found := false
		for _, id := range ids {
			if id == p.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ID %q not found in BuiltinPersonaIDs()", p.ID)
		}
	}
}

func TestIsValidBuiltinID(t *testing.T) {
	tests := []struct {
		id       string
		expected bool
	}{
		{"catgirl", true},
		{"butler", true},
		{"nonexistent", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := IsValidBuiltinID(tt.id); got != tt.expected {
				t.Errorf("IsValidBuiltinID(%q) = %v, want %v", tt.id, got, tt.expected)
			}
		})
	}
}

func TestPersonaTimestamps(t *testing.T) {
	now := time.Now()
	p := Persona{
		ID:        "test",
		Name:      "测试",
		Prompt:    "测试提示词",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if !p.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt not preserved")
	}
	if !p.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt not preserved")
	}
}