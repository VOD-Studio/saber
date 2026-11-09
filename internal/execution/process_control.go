package execution

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

func validateProcessInput(args map[string]any) error {
	action, _ := args["action"].(string)
	fields := map[string][]string{"start": {"action", "command", "stdin", "pty", "rows", "cols", "notify", "watch_patterns", "heartbeat_seconds"}, "list": {"action"}, "notifications": {"action"}, "status": {"action", "id"}, "kill": {"action", "id"}, "wait": {"action", "id", "wait_seconds"}, "logs": {"action", "id", "offset", "limit"}, "write": {"action", "id", "data"}, "close_stdin": {"action", "id"}, "resize": {"action", "id", "rows", "cols"}, "signal": {"action", "id", "signal"}}
	allowed, ok := fields[action]
	if !ok {
		return fmt.Errorf("unknown process action %q", action)
	}
	for key := range args {
		found := false
		for _, field := range allowed {
			if field == key {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("argument %q is not valid for process action %q", key, action)
		}
	}
	if action != "start" && action != "list" && action != "notifications" {
		id, ok := args["id"].(string)
		if !ok || id == "" || strings.ContainsRune(id, 0) {
			return errors.New("process id is required")
		}
	}
	if action == "start" {
		command, ok := args["command"].(string)
		if !ok || command == "" || strings.ContainsRune(command, 0) {
			return errors.New("command is required")
		}
	}
	for _, key := range []string{"stdin", "data"} {
		if value, ok := args[key]; ok {
			text, valid := value.(string)
			if !valid || len(text) > 1<<20 {
				return fmt.Errorf("%s must be a string of at most 1 MiB", key)
			}
		}
	}
	if action == "write" {
		if _, ok := args["data"].(string); !ok {
			return errors.New("write requires data")
		}
	}
	for _, key := range []string{"pty", "notify"} {
		if value, ok := args[key]; ok {
			if _, valid := value.(bool); !valid {
				return fmt.Errorf("%s must be boolean", key)
			}
		}
	}
	for _, key := range []string{"rows", "cols"} {
		if err := validateInteger(args, key, 1, 1000); err != nil {
			return err
		}
	}
	if action == "resize" {
		if args["rows"] == nil || args["cols"] == nil {
			return errors.New("resize requires rows and cols")
		}
	}
	if err := validateInteger(args, "heartbeat_seconds", 1, 3600); err != nil {
		return err
	}
	if err := validateInteger(args, "wait_seconds", 0, processWaitMax); err != nil {
		return err
	}
	if err := validateInteger(args, "offset", 0, 0); err != nil {
		return err
	}
	if err := validateInteger(args, "limit", outputPageMin, outputPageMax); err != nil {
		return err
	}
	if action == "signal" {
		name, ok := args["signal"].(string)
		if !ok || processSignals[name] == 0 {
			return errors.New("signal must be HUP, INT, TERM, KILL, USR1, USR2, STOP or CONT")
		}
	}
	if value, ok := args["watch_patterns"]; ok {
		list, valid := value.([]any)
		if !valid || len(list) > 16 {
			return errors.New("watch_patterns must contain at most 16 expressions")
		}
		for _, value := range list {
			pattern, ok := value.(string)
			if !ok || pattern == "" || len(pattern) > 256 {
				return errors.New("watch pattern must be 1..256 bytes")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("invalid watch pattern: %w", err)
			}
		}
	}
	return nil
}

var processSignals = map[string]int{"HUP": 1, "INT": 2, "TERM": 15, "KILL": 9, "USR1": 10, "USR2": 12, "STOP": 19, "CONT": 18}

func processOptions(args map[string]any, command string) (launchOptions, []*regexp.Regexp, int, bool) {
	options := launchOptions{Command: command, Rows: 24, Cols: 80}
	options.PTY, _ = args["pty"].(bool)
	if value, ok := args["rows"]; ok {
		options.Rows, _ = intArgument(value)
	}
	if value, ok := args["cols"]; ok {
		options.Cols, _ = intArgument(value)
	}
	patterns := []*regexp.Regexp{}
	if list, ok := args["watch_patterns"].([]any); ok {
		for _, value := range list {
			patterns = append(patterns, regexp.MustCompile(value.(string)))
		}
	}
	heartbeat := 0
	if value, ok := args["heartbeat_seconds"]; ok {
		heartbeat, _ = intArgument(value)
	}
	notify := true
	if value, ok := args["notify"].(bool); ok {
		notify = value
	}
	return options, patterns, heartbeat, notify
}

func (e *Executor) controlProcess(ctx context.Context, args map[string]any, run scope) (Result, error) {
	record, err := e.processFor(run.id, args)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	if !record.running() {
		return Result{ExitCode: -1}, errors.New("process has exited; stdin and signals cannot be replayed")
	}
	process, ok := record.proc.(interactiveProcess)
	if !ok {
		return Result{ExitCode: -1}, errors.New("process has no interaction transport")
	}
	action, _ := args["action"].(string)
	data := map[string]any{"id": record.id, "action": action}
	switch action {
	case "write":
		text, _ := args["data"].(string)
		var written int
		written, err = process.Write([]byte(text))
		data["bytes_written"] = written
	case "close_stdin":
		err = process.CloseWrite()
	case "resize":
		if !record.pty {
			return Result{ExitCode: -1}, errors.New("process has no PTY")
		}
		rows, _ := intArgument(args["rows"])
		cols, _ := intArgument(args["cols"])
		err = process.Resize(rows, cols)
	case "signal":
		name, _ := args["signal"].(string)
		err = process.Signal(processSignals[name])
	}
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	return Result{ExitCode: 0, Summary: "process " + record.id + " " + action, Data: data}, nil
}
