// Package mcp 为 MCP (Model Context Protocol) 工具提供验证和错误处理工具。
package mcp

import (
	"fmt"
)

// ValidationError 表示应该返回给用户的验证错误。
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// SystemError 表示内部系统错误。
type SystemError struct {
	Op      string
	Err     error
	Message string
}

func (e *SystemError) Error() string {
	return fmt.Sprintf("%s: %s: %v", e.Op, e.Message, e.Err)
}

func (e *SystemError) Unwrap() error {
	return e.Err
}

// IsValidationError 检查一个错误是否为验证错误。
func IsValidationError(err error) bool {
	_, ok := err.(*ValidationError)
	return ok
}

// WrapSystemError 使用上下文包装内部错误。
func WrapSystemError(err error, op, message string) error {
	return &SystemError{
		Op:      op,
		Err:     err,
		Message: message,
	}
}

// ValidateToolInput 根据 JSON schema 验证工具输入。
func ValidateToolInput(params map[string]any, schema map[string]any) error {
	// 基础验证 - 检查必填字段
	required, ok := schema["required"].([]interface{})
	if !ok {
		return nil
	}

	for _, r := range required {
		field, ok := r.(string)
		if !ok {
			continue
		}
		if _, exists := params[field]; !exists {
			return &ValidationError{
				Field:   field,
				Message: "必填字段缺失",
			}
		}
	}

	return nil
}
