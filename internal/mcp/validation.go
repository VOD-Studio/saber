// Package mcp provides validation and error handling utilities for MCP (Model Context Protocol) tools.
package mcp

import (
	"fmt"
)

// ValidationError represents a validation error that should be returned to the user.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// SystemError represents an internal system error.
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

// IsValidationError checks if an error is a validation error.
func IsValidationError(err error) bool {
	_, ok := err.(*ValidationError)
	return ok
}

// WrapSystemError wraps an internal error with context.
func WrapSystemError(err error, op, message string) error {
	return &SystemError{
		Op:      op,
		Err:     err,
		Message: message,
	}
}

// ValidateToolInput validates tool input against a JSON schema.
func ValidateToolInput(params map[string]any, schema map[string]any) error {
	// Basic validation - check required fields
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
				Message: "required field is missing",
			}
		}
	}

	return nil
}
