package slashcmd

import (
	"fmt"
	"io"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type Command struct {
	Name        string
	Description string
	BrowserOnly bool
}

var commands = []Command{
	{Name: "/copy", Description: "Copy selected files or paths"},
	{Name: "/delete", Description: "Delete selected files or paths"},
	{Name: "/download", Description: "Download kabibi.bat", BrowserOnly: true},
	{Name: "/edit", Description: "Edit a selected file or path"},
	{Name: "/model", Description: "Choose an AI provider and model"},
	{Name: "/move", Description: "Move selected files or paths"},
	{Name: "/panels", Description: "Show or hide the file panels"},
	{Name: "/remove", Description: "Alias for /delete"},
	{Name: "/rename", Description: "Rename a selected file or path"},
	{Name: "/settings", Description: "Open settings"},
}

type Invocation struct {
	Name string
	Args []string
}

func InputToken(input string) (token, args string, isSlash bool) {
	value := strings.TrimLeft(input, " \t\r\n")
	if !strings.HasPrefix(value, "/") {
		return "", "", false
	}
	end := strings.IndexAny(value, " \t\r\n")
	if end < 0 {
		return value, "", true
	}
	return value[:end], strings.TrimSpace(value[end:]), true
}

func Commands(browser bool) []Command {
	result := make([]Command, 0, len(commands))
	for _, command := range commands {
		if command.BrowserOnly && !browser {
			continue
		}
		result = append(result, command)
	}
	return result
}

func Lookup(name string) (Command, bool) {
	name = strings.ToLower(name)
	for _, command := range commands {
		if command.Name == name {
			return command, true
		}
	}
	return Command{}, false
}

func Matches(prefix string, browser bool) []Command {
	prefix = strings.ToLower(prefix)
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	var matches []Command
	for _, command := range Commands(browser) {
		if strings.HasPrefix(command.Name, prefix) {
			matches = append(matches, command)
		}
	}
	return matches
}

// Parse recognizes a slash-command line. Shell quoting is supported for path
// arguments, while pipelines, compound commands, and expansions are rejected.
func Parse(line string) (Invocation, bool, error) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") {
		return Invocation{}, false, nil
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(trimmed), "")
	if err != nil {
		return Invocation{}, true, fmt.Errorf("invalid slash command syntax: %w", err)
	}
	if len(file.Stmts) != 1 {
		return Invocation{}, true, fmt.Errorf("slash commands must be a single command")
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || len(stmt.Redirs) > 0 ||
		len(call.Assigns) > 0 || len(call.Args) == 0 {
		return Invocation{}, true, fmt.Errorf("slash commands do not support shell operators or redirections")
	}

	name, literal := wordLiteral(call.Args[0])
	if !literal {
		return Invocation{}, true, fmt.Errorf("slash command name must be a literal")
	}
	if !strings.HasPrefix(name, "/") {
		return Invocation{}, true, fmt.Errorf("invalid slash command name")
	}
	invocation := Invocation{Name: strings.ToLower(name)}
	for _, word := range call.Args[1:] {
		value, literal := wordLiteral(word)
		if !literal || value == "" {
			return Invocation{}, true, fmt.Errorf("slash command arguments must be non-empty literal values")
		}
		invocation.Args = append(invocation.Args, value)
	}
	return invocation, true, nil
}

func wordLiteral(word *syntax.Word) (string, bool) {
	var value strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			value.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
			value.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, quotedPart := range part.Parts {
				literal, ok := quotedPart.(*syntax.Lit)
				if !ok {
					return "", false
				}
				value.WriteString(literal.Value)
			}
		default:
			return "", false
		}
	}
	return value.String(), true
}

func UsageError(name, usage string) error {
	usage = strings.TrimSpace(usage)
	if usage == "" {
		return fmt.Errorf("usage: %s", name)
	}
	return fmt.Errorf("usage: %s %s", name, usage)
}

func WriteError(w io.Writer, err error) error {
	if err == nil {
		return nil
	}
	_, writeErr := fmt.Fprintf(w, "%v\n", err)
	if writeErr != nil {
		return fmt.Errorf("write slash command error: %w", writeErr)
	}
	return nil
}
