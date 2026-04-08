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

// ValidateToolInput 根据 JSON schema 验证工具输入。
func ValidateToolInput(params map[string]any, schema map[string]any) error {
	if schema == nil {
		return nil
	}

	// 获取 properties 定义
	properties, _ := schema["properties"].(map[string]any)

	// 1. 检查必填字段
	required, ok := schema["required"].([]interface{})
	if ok {
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
	}

	// 2. 验证每个字段的值
	for fieldName, fieldValue := range params {
		fieldSchema, ok := properties[fieldName].(map[string]any)
		if !ok {
			continue // 没有 schema 定义，跳过验证
		}

		if err := validateValue(fieldName, fieldValue, fieldSchema); err != nil {
			return err
		}
	}

	return nil
}

// validateValue 验证单个值是否符合 schema 定义
func validateValue(fieldName string, value any, schema map[string]any) error {
	// 类型验证
	fieldType, ok := schema["type"].(string)
	if ok && fieldType != "" {
		if err := validateType(fieldName, value, fieldType); err != nil {
			return err
		}
	}

	// 根据类型进行特定验证
	switch fieldType {
	case "string":
		if err := validateString(fieldName, value, schema); err != nil {
			return err
		}
	case "number", "integer":
		if err := validateNumber(fieldName, value, schema); err != nil {
			return err
		}
	case "array":
		if err := validateArray(fieldName, value, schema); err != nil {
			return err
		}
	case "object":
		if err := validateObject(fieldName, value, schema); err != nil {
			return err
		}
	}

	// enum 验证（适用于所有类型）
	if err := validateEnum(fieldName, value, schema); err != nil {
		return err
	}

	return nil
}

// validateType 验证值的类型
func validateType(fieldName string, value any, expectedType string) error {
	var valid bool
	switch expectedType {
	case "string":
		valid = isString(value)
	case "number":
		valid = isNumber(value)
	case "integer":
		valid = isInteger(value)
	case "boolean":
		valid = isBoolean(value)
	case "array":
		valid = isArray(value)
	case "object":
		valid = isObject(value)
	default:
		return nil // 未知类型，跳过验证
	}

	if !valid {
		return &ValidationError{
			Field:   fieldName,
			Message: fmt.Sprintf("类型错误，期望 %s", expectedType),
		}
	}
	return nil
}

// validateString 验证字符串约束
func validateString(fieldName string, value any, schema map[string]any) error {
	str, ok := value.(string)
	if !ok {
		return nil
	}

	length := len(str)

	// minLength 验证
	if minLength, ok := schema["minLength"].(float64); ok {
		if length < int(minLength) {
			return &ValidationError{
				Field:   fieldName,
				Message: fmt.Sprintf("字符串长度 %d 小于最小长度 %d", length, int(minLength)),
			}
		}
	}

	// maxLength 验证
	if maxLength, ok := schema["maxLength"].(float64); ok {
		if length > int(maxLength) {
			return &ValidationError{
				Field:   fieldName,
				Message: fmt.Sprintf("字符串长度 %d 超过最大长度 %d", length, int(maxLength)),
			}
		}
	}

	return nil
}

// validateNumber 验证数字约束
func validateNumber(fieldName string, value any, schema map[string]any) error {
	num, ok := toFloat64(value)
	if !ok {
		return nil
	}

	// minimum 验证
	if minimum, ok := schema["minimum"].(float64); ok {
		if num < minimum {
			return &ValidationError{
				Field:   fieldName,
				Message: fmt.Sprintf("数值 %.2f 小于最小值 %.2f", num, minimum),
			}
		}
	}

	// maximum 验证
	if maximum, ok := schema["maximum"].(float64); ok {
		if num > maximum {
			return &ValidationError{
				Field:   fieldName,
				Message: fmt.Sprintf("数值 %.2f 超过最大值 %.2f", num, maximum),
			}
		}
	}

	return nil
}

// validateArray 验证数组约束
func validateArray(fieldName string, value any, schema map[string]any) error {
	arr, ok := value.([]interface{})
	if !ok {
		// 尝试 []any
		arr, ok = value.([]any)
		if !ok {
			return nil
		}
	}

	// minItems 验证
	if minItems, ok := schema["minItems"].(float64); ok {
		if len(arr) < int(minItems) {
			return &ValidationError{
				Field:   fieldName,
				Message: fmt.Sprintf("数组长度 %d 小于最小长度 %d", len(arr), int(minItems)),
			}
		}
	}

	// maxItems 验证
	if maxItems, ok := schema["maxItems"].(float64); ok {
		if len(arr) > int(maxItems) {
			return &ValidationError{
				Field:   fieldName,
				Message: fmt.Sprintf("数组长度 %d 超过最大长度 %d", len(arr), int(maxItems)),
			}
		}
	}

	// 验证数组元素
	itemsSchema, ok := schema["items"].(map[string]any)
	if ok {
		for i, item := range arr {
			if err := validateValue(fmt.Sprintf("%s[%d]", fieldName, i), item, itemsSchema); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateObject 验证对象约束
func validateObject(fieldName string, value any, schema map[string]any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	// 递归验证嵌套对象
	return ValidateToolInput(obj, schema)
}

// validateEnum 验证枚举值
func validateEnum(fieldName string, value any, schema map[string]any) error {
	enum, ok := schema["enum"].([]interface{})
	if !ok {
		return nil
	}

	for _, allowed := range enum {
		if value == allowed {
			return nil
		}
	}

	return &ValidationError{
		Field:   fieldName,
		Message: fmt.Sprintf("值 %v 不在允许的范围内 %v", value, enum),
	}
}

// 类型检查辅助函数

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func isNumber(v any) bool {
	switch v.(type) {
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func isInteger(v any) bool {
	switch v := v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float64:
		// 检查是否是无小数的浮点数
		return v == float64(int64(v))
	case float32:
		return v == float32(int32(v))
	default:
		return false
	}
}

func isBoolean(v any) bool {
	_, ok := v.(bool)
	return ok
}

func isArray(v any) bool {
	_, ok := v.([]interface{})
	return ok
}

func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

// toFloat64 将数值转换为 float64
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}
