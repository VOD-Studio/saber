# Persona 人格管理模块

## 概述

Persona 模块为 Saber 机器人提供人格管理功能，允许用户为不同房间设置不同的机器人"性格"。每个人格包含独特的系统提示词，会在 AI 对话时与基础系统提示词合并。

## 功能特性

- **每房间独立人格**：每个 Matrix 房间可以独立设置不同的人格
- **内置人格**：提供 5 个预设人格（猫娘、管家、海盗、傲娇、诗人）
- **自定义人格**：用户可以创建、删除自定义人格
- **提示词合并**：人格提示词与配置文件中的 system_prompt 自动合并

## 文件结构

```
internal/persona/
├── types.go           # Persona 结构体定义
├── builtin.go         # 内置人格定义
├── service.go         # 核心服务实现（CRUD、房间映射）
├── commands.go        # Matrix 命令处理器
└── *_test.go          # 单元测试
```

## 数据库设计

### personas 表

| 字段 | 类型 | 说明 |
|------|------|------|
| id | TEXT | 人格唯一标识符（主键） |
| name | TEXT | 显示名称 |
| prompt | TEXT | 系统提示词 |
| description | TEXT | 人格描述 |
| is_builtin | INTEGER | 是否内置人格（0/1） |
| created_at | INTEGER | 创建时间戳 |
| updated_at | INTEGER | 更新时间戳 |

### room_personas 表

| 字段 | 类型 | 说明 |
|------|------|------|
| room_id | TEXT | Matrix 房间 ID（主键） |
| persona_id | TEXT | 激活的人格 ID（外键） |
| updated_at | INTEGER | 更新时间戳 |

## 内置人格

| ID | 名称 | 描述 |
|---|---|---|
| catgirl | 猫娘 | 可爱活泼，句尾加"喵～" |
| butler | 管家 | 优雅恭敬，英伦风格 |
| pirate | 海盗 | 豪爽冒险，说话带"啊哈" |
| tsundere | 傲娇 | 表面冷淡内心温柔 |
| poet | 诗人 | 文雅古风，喜用诗词 |

## 命令列表

| 命令 | 描述 |
|------|------|
| `!persona list` | 列出所有可用人格 |
| `!persona set <id>` | 设置当前房间的人格 |
| `!persona clear` | 清除当前房间的人格 |
| `!persona status` | 显示当前房间的人格状态 |
| `!persona new <id> "<name>" "<prompt>" "<desc>"` | 创建新人格 |
| `!persona del <id>` | 删除自定义人格 |

## 核心接口

### Service

```go
type Service struct {
    db           *sql.DB
    personas     map[string]*Persona      // 人格缓存
    roomPersonas map[id.RoomID]string     // 房间人格映射缓存
    mu           sync.RWMutex
}

// 主要方法
func NewService(dbPath string) (*Service, error)
func (s *Service) List() []*Persona
func (s *Service) Get(id string) *Persona
func (s *Service) Create(id, name, prompt, description string) error
func (s *Service) Delete(id string) error
func (s *Service) GetRoomPersona(roomID id.RoomID) *Persona
func (s *Service) SetRoomPersona(ctx context.Context, roomID id.RoomID, personaID string) error
func (s *Service) ClearRoomPersona(ctx context.Context, roomID id.RoomID) error
func (s *Service) GetSystemPrompt(roomID id.RoomID, basePrompt string) string
```

### PersonaService 接口（用于 AI 服务集成）

```go
type PersonaService interface {
    GetSystemPrompt(roomID id.RoomID, basePrompt string) string
}
```

## 提示词合并逻辑

```go
func (s *Service) GetSystemPrompt(roomID id.RoomID, basePrompt string) string {
    persona := s.GetRoomPersona(roomID)
    if persona == nil {
        return basePrompt  // 无人格，返回基础提示词
    }
    if basePrompt == "" {
        return persona.Prompt  // 无基础提示词，仅返回人格提示词
    }
    // 合并：基础提示词 + 分隔线 + 人格提示词
    return basePrompt + "\n\n---\n\n" + persona.Prompt
}
```

## 使用示例

### 设置房间人格

```
用户: !persona set catgirl
机器人: ✅ 已将当前房间的人格设置为 猫娘 (catgirl)
```

### 查看人格列表

```
用户: !persona list
机器人: 🎭 可用人格列表

  catgirl (猫娘) - 可爱的猫娘，会在句尾加"喵" [内置] ✓ [当前]
  butler (管家) - 优雅的英式管家，恭敬专业 [内置]
  ...
```

### 创建自定义人格

```
用户: !persona new robot "机器人" "你是一个友好的机器人助手" "机器人人格"
机器人: ✅ 已创建人格 机器人 (robot)
```

## 注意事项

1. **内置人格不可删除**：尝试删除内置人格会返回错误
2. **人格 ID 唯一性**：创建新人格时不能与已有 ID 冲突
3. **删除人格后房间映射清理**：删除人格时会自动清除使用该人格的房间映射
4. **数据库位置**：persona.db 默认存储在配置文件同目录下

## 扩展建议

- 支持人格提示词变量替换
- 支持人格预览功能
- 支持导入/导出人格配置
- 支持人格使用统计