<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# persona

## Purpose

Persona 人格管理模块，为 Saber 机器人提供人格管理功能。每个 Matrix 房间可设置不同的机器人"性格"，人格提示词与基础系统提示词自动合并。

## Key Files

| File | Description |
|------|-------------|
| `types.go` | Persona 结构体定义和接口 |
| `builtin.go` | 内置人格定义（猫娘、管家、海盗、傲娇、诗人） |
| `service.go` | 核心服务实现：CRUD 操作、房间映射、提示词合并 |
| `commands.go` | Matrix 命令处理器（`!persona` 系列） |

## Subdirectories

无

## For AI Agents

### Working In This Directory

- **数据库位置**: persona.db 默认存储在配置文件同目录
- **内置人格不可删除**: `IsBuiltin=true` 的人格无法通过命令删除
- **缓存机制**: `Service` 使用 `sync.RWMutex` 保护内存缓存

### Testing Requirements

- 使用临时目录创建测试数据库
- 测试人格 CRUD 和房间映射功能

### Common Patterns

#### 获取合并后的系统提示词

```go
prompt := personaService.GetSystemPrompt(roomID, basePrompt)
// 返回: basePrompt + "\n\n---\n\n" + persona.Prompt
```

#### 设置房间人格

```go
err := personaService.SetRoomPersona(ctx, roomID, "catgirl")
```

## 内置人格列表

| ID | 名称 | 描述 |
|----|------|------|
| `catgirl` | 猫娘 | 可爱活泼，句尾加"喵～" |
| `butler` | 管家 | 优雅恭敬，英伦风格 |
| `pirate` | 海盗 | 豪爽冒险，说话带"啊哈" |
| `tsundere` | 傲娇 | 表面冷淡内心温柔 |
| `poet` | 诗人 | 文雅古风，喜用诗词 |

## 命令列表

| 命令 | 描述 |
|------|------|
| `!persona list` | 列出所有可用人格 |
| `!persona set <id>` | 设置当前房间的人格 |
| `!persona clear` | 清除当前房间的人格 |
| `!persona status` | 显示当前房间的人格状态 |
| `!persona new <id> "<name>" "<prompt>" "<desc>"` | 创建新人格 |
| `!persona del <id>` | 删除自定义人格 |

## Dependencies

### Internal

- `rua.plus/saber/internal/matrix` - Matrix 消息发送接口

### External

- `maunium.net/go/mautrix/id` - Matrix ID 类型
- `modernc.org/sqlite` - SQLite 驱动

<!-- MANUAL: 以下是手动添加的额外说明 -->

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