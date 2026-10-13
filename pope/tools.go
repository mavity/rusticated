package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"ai"
)

// Tools maps each tool name to its implementation, a function of the form
//
//	func(ctx context.Context, in T, feedback LogFeedback) (any, error)
//
// where T is a struct describing the arguments: a blank `_ struct{}` field
// carries the tool description in its `doc` tag, and every exported field is a
// parameter documented by its `doc` tag. A parameter is named by its `json`
// tag, or by its lowercased field name, and is required unless the tag has
// `omitempty`.
type LogFeedback func(text string)

var Tools = map[string]any{
	"read_file":  ReadFile,
	"write_file": WriteFile,
	"exec":       Exec,
}

const maxToolOutput = 32768

func truncateOutput(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n...[truncated]"
	}
	return s
}

func compactString(s string, maxLen int) string {
	runes := []rune(strings.TrimSpace(s))
	if maxLen <= 0 || len(runes) <= maxLen {
		return string(runes)
	}
	if maxLen == 1 {
		return "…"
	}
	return string(runes[:maxLen-1]) + "…"
}

func toolLead(input any, primaryArg string, maxArgLen int) string {
	in := reflect.TypeOf(input)
	for i := range in.NumField() {
		field := in.Field(i)
		if field.Name == "_" {
			return field.Tag.Get("emoji") + " " + compactString(primaryArg, maxArgLen) + " ... "
		}
	}
	panic(fmt.Sprintf("tool input %v has no metadata field", in))
}

// toolInputType returns the argument struct type T of a tool function. A tool
// with any other shape is a programming error, so it panics.
func toolInputType(name string, fn any) reflect.Type {
	t := reflect.TypeOf(fn)
	if t == nil || t.Kind() != reflect.Func || t.NumIn() != 3 || t.NumOut() != 2 ||
		!reflect.TypeFor[context.Context]().AssignableTo(t.In(0)) ||
		t.In(1).Kind() != reflect.Struct ||
		!reflect.TypeFor[LogFeedback]().AssignableTo(t.In(2)) ||
		t.Out(0).Kind() != reflect.Interface || t.Out(0).NumMethod() != 0 ||
		t.Out(1) != reflect.TypeFor[error]() {
		panic(fmt.Sprintf("tool %q must be func(context.Context, struct, LogFeedback) (any, error), got %v", name, t))
	}
	return t.In(1)
}

// GetToolDeclarations builds the LiteRT-LM tool declarations from Tools, sorted
// by name so the declarations are identical from run to run.
func GetToolDeclarations() []map[string]any {
	names := make([]string, 0, len(Tools))
	for name := range Tools {
		names = append(names, name)
	}
	sort.Strings(names)

	decls := make([]map[string]any, 0, len(names))
	for _, name := range names {
		in := toolInputType(name, Tools[name])
		description := ""
		properties := map[string]any{}
		required := []string{}
		for i := range in.NumField() {
			f := in.Field(i)
			if f.Name == "_" {
				description = f.Tag.Get("doc")
				continue
			}
			if !f.IsExported() {
				continue
			}
			param, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if param == "-" {
				continue
			}
			if param == "" {
				param = strings.ToLower(f.Name)
			}
			schema := typeSchema(name, f.Type)
			if doc := f.Tag.Get("doc"); doc != "" {
				schema["description"] = doc
			}
			properties[param] = schema
			if !strings.Contains(opts, "omitempty") {
				required = append(required, param)
			}
		}
		parameters := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			parameters["required"] = required
		}
		decls = append(decls, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": description,
				"parameters":  parameters,
			},
		})
	}
	return decls
}

func typeSchema(tool string, t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": typeSchema(tool, t.Elem())}
	}
	panic(fmt.Sprintf("tool %q: unsupported parameter type %v", tool, t))
}

// ExecuteTool decodes the call's raw JSON arguments straight into the tool's
// argument struct and runs the tool. Failures are returned as {"error": ...}
// for the model to read.
func ExecuteTool(ctx context.Context, call ai.ToolCall, feedback LogFeedback) any {
	fn, ok := Tools[call.Name]
	if !ok {
		return map[string]any{"error": fmt.Sprintf("unknown tool: %s", call.Name)}
	}
	in := reflect.New(toolInputType(call.Name, fn))
	if len(call.RawArguments) > 0 {
		if err := json.Unmarshal(call.RawArguments, in.Interface()); err != nil {
			return map[string]any{"error": "invalid arguments: " + err.Error()}
		}
	}
	feedbackValue := reflect.Zero(reflect.TypeFor[LogFeedback]())
	if feedback != nil {
		feedbackValue = reflect.ValueOf(feedback)
	}
	out := reflect.ValueOf(fn).Call([]reflect.Value{reflect.ValueOf(ctx), in.Elem(), feedbackValue})
	if err, _ := out[1].Interface().(error); err != nil {
		return map[string]any{"error": err.Error()}
	}
	return out[0].Interface()
}
