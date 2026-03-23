// Package ai 提供 AI 服务相关功能。
package ai

import (
	"fmt"
	"sort"
	"sync"

	"rua.plus/saber/internal/config"
)

// ModelInfo 存储单个模型的元数据信息。
type ModelInfo struct {
	// ID 是模型的唯一标识符（对应配置中的 map key）。
	ID string
	// Model 是实际使用的模型名称（如 gpt-4o-mini）。
	Model string
	// IsConfigDefault 表示该模型是否为配置文件中指定的默认模型。
	IsConfigDefault bool
}

// ModelRegistry 管理模型注册和默认模型切换。
//
// 它提供了运行时模型切换功能，允许用户动态更改默认模型，
// 而无需修改配置文件或重启服务。
//
// 线程安全：所有方法都是并发安全的。
type ModelRegistry struct {
	mu sync.RWMutex

	// configDefault 是配置文件中指定的默认模型（不可变）。
	configDefault string
	// currentDefault 是当前运行时使用的默认模型。
	currentDefault string
	// models 存储所有已注册的模型信息。
	models map[string]ModelInfo
	// globalConfig 是 AI 全局配置的引用。
	globalConfig *config.AIConfig
}

// NewModelRegistry 创建一个新的模型注册表。
//
// 参数:
//   - cfg: AI 配置，用于初始化模型列表和默认模型
//
// 返回值:
//   - *ModelRegistry: 创建的模型注册表
func NewModelRegistry(cfg *config.AIConfig) *ModelRegistry {
	if cfg == nil {
		return &ModelRegistry{
			models: make(map[string]ModelInfo),
		}
	}

	models := make(map[string]ModelInfo)

	// 从 Providers 收集所有模型
	for providerName, providerCfg := range cfg.Providers {
		for modelName, modelCfg := range providerCfg.Models {
			fullID := config.FormatModelID(providerName, modelName)
			actualModel := modelCfg.Model
			if actualModel == "" {
				actualModel = modelName
			}
			models[fullID] = ModelInfo{
				ID:              fullID,
				Model:           actualModel,
				IsConfigDefault: fullID == cfg.DefaultModel,
			}
		}
	}

	// 将配置文件中的默认模型添加到注册表（如果尚未添加）
	if cfg.DefaultModel != "" {
		if _, exists := models[cfg.DefaultModel]; !exists {
			// 解析默认模型
			_, model, err := config.ParseModelID(cfg.DefaultModel)
			if err == nil {
				models[cfg.DefaultModel] = ModelInfo{
					ID:              cfg.DefaultModel,
					Model:           model,
					IsConfigDefault: true,
				}
			} else {
				// 旧格式，直接使用
				models[cfg.DefaultModel] = ModelInfo{
					ID:              cfg.DefaultModel,
					Model:           cfg.DefaultModel,
					IsConfigDefault: true,
				}
			}
		}
	}

	// 添加 Models map 中定义的模型别名（向后兼容）
	for alias, modelConfig := range cfg.Models {
		// 如果别名已经是完全限定格式，直接使用
		if _, _, err := config.ParseModelID(alias); err == nil {
			if _, exists := models[alias]; !exists {
				models[alias] = ModelInfo{
					ID:              alias,
					Model:           modelConfig.Model,
					IsConfigDefault: alias == cfg.DefaultModel,
				}
			}
		}
		// 别名作为快捷方式保留在 Models map 中，由 GetModelConfig 处理
	}

	return &ModelRegistry{
		configDefault:  cfg.DefaultModel,
		currentDefault: cfg.DefaultModel,
		models:         models,
		globalConfig:   cfg,
	}
}

// SetDefault 切换当前默认模型。
//
// 参数:
//   - modelID: 要设置为默认的模型标识符
//
// 返回值:
//   - error: 如果模型不存在则返回错误
func (r *ModelRegistry) SetDefault(modelID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 允许设置任意模型名（即使不在预定义列表中）
	// 这样用户可以使用不在 Models map 中的模型
	r.currentDefault = modelID

	return nil
}

// GetDefault 获取当前默认模型。
//
// 返回值:
//   - string: 当前默认模型的标识符
func (r *ModelRegistry) GetDefault() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.currentDefault
}

// GetConfigDefault 获取配置文件中指定的默认模型。
//
// 该值是不可变的，用于在服务重启时恢复默认模型。
//
// 返回值:
//   - string: 配置文件中指定的默认模型标识符
func (r *ModelRegistry) GetConfigDefault() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.configDefault
}

// ListModels 列出所有已注册的模型。
//
// 返回值按模型 ID 字母顺序排序。
//
// 返回值:
//   - []ModelInfo: 模型信息列表
func (r *ModelRegistry) ListModels() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ModelInfo, 0, len(r.models))
	for _, info := range r.models {
		result = append(result, info)
	}

	// 按模型 ID 排序
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})

	return result
}

// GetModelInfo 获取指定模型的信息。
//
// 参数:
//   - modelID: 模型标识符
//
// 返回值:
//   - ModelInfo: 模型信息
//   - bool: 模型是否存在
func (r *ModelRegistry) GetModelInfo(modelID string) (ModelInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.models[modelID]
	return info, exists
}

// IsCurrentDefault 检查指定模型是否为当前默认模型。
//
// 参数:
//   - modelID: 模型标识符
//
// 返回值:
//   - bool: 是否为当前默认模型
func (r *ModelRegistry) IsCurrentDefault(modelID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.currentDefault == modelID
}

// ResetDefault 重置默认模型为配置文件中的值。
func (r *ModelRegistry) ResetDefault() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.currentDefault = r.configDefault
}

// RegisterModel 注册一个新模型到注册表。
//
// 参数:
//   - id: 模型标识符
//   - model: 实际模型名称
func (r *ModelRegistry) RegisterModel(id, model string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	info := ModelInfo{
		ID:              id,
		Model:           model,
		IsConfigDefault: id == r.configDefault,
	}
	r.models[id] = info
}

// ValidateModel 验证模型是否可用。
//
// 模型可用是指：
// 1. 模型在 Models map 中有定义，或
// 2. 模型可以与全局配置一起使用（任意模型名）
//
// 参数:
//   - modelID: 模型标识符
//
// 返回值:
//   - error: 如果模型不可用则返回错误
func (r *ModelRegistry) ValidateModel(modelID string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 检查模型是否在预定义列表中
	if _, exists := r.models[modelID]; exists {
		return nil
	}

	// 如果不在预定义列表中，检查全局配置是否允许使用任意模型
	if r.globalConfig == nil {
		return fmt.Errorf("模型 %q 不在可用模型列表中", modelID)
	}

	// 全局配置存在，允许使用任意模型名（将使用全局配置）
	return nil
}
