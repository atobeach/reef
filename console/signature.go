package console

import (
	"fmt"
	"regexp"
	"strings"
)

// Definition is a parsed command signature: its name, arguments and options.
type Definition struct {
	Name      string
	Arguments []Argument
	Options   []Option
}

// Argument is a positional `{name}` in a signature.
type Argument struct {
	Name        string
	Description string
	Required    bool
	// Array collects every remaining positional value (`{name*}`).
	Array   bool
	Default []string
}

// Option is a `{--name}` in a signature.
type Option struct {
	Name        string
	Shortcut    string
	Description string
	// AcceptsValue is false for a flag (`{--force}`): presence means true.
	AcceptsValue bool
	// Array allows the option several times (`{--model=*}`).
	Array   bool
	Default []string
}

// The signature grammar's pieces.
var (
	signatureName   = regexp.MustCompile(`^\s*([^\s{]+)`)
	signatureTokens = regexp.MustCompile(`(?s)\{\s*(.*?)\s*\}`)
	descriptionSep  = regexp.MustCompile(`\s+:\s+`)
	whitespace      = regexp.MustCompile(`\s+`)
)

// ParseSignature parses a command signature: the name, then `{argument}` and
// `{--option}` tokens, each with an optional ` : description`. `?` makes an
// argument optional, `*` an array, `=value` a default; `--S|name` gives an
// option a shortcut and a trailing `=` makes it take a value:
//
//	bookings:send-reminders
//	flights:refresh {airport? : Airport code}
//	airports:import {path} {--output= : Output path}
//	model:prune {--model=* : Models to prune} {--pretend}
//	queue:work {connection?} {--Q|queue=default}
func ParseSignature(signature string) (Definition, error) {
	match := signatureName.FindStringSubmatch(signature)
	if match == nil {
		return Definition{}, fmt.Errorf("console: unable to determine command name from signature %q", signature)
	}

	definition := Definition{Name: match[1]}

	for _, token := range signatureTokens.FindAllStringSubmatch(signature, -1) {
		body, description := splitDescription(token[1])

		if rest, ok := strings.CutPrefix(body, "--"); ok {
			definition.Options = append(definition.Options, parseOption(rest, description))

			continue
		}

		definition.Arguments = append(definition.Arguments, parseArgument(body, description))
	}

	return definition, nil
}

// splitDescription splits "body : description", folding the description's
// whitespace (signatures span lines).
func splitDescription(token string) (body, description string) {
	parts := descriptionSep.Split(strings.TrimSpace(token), 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), whitespace.ReplaceAllString(strings.TrimSpace(parts[1]), " ")
	}

	return strings.TrimSpace(parts[0]), ""
}

// parseArgument parses an argument token: `name`, `name?`, `name*`,
// `name?*`, `name=default` or `name=*a,b`.
func parseArgument(token, description string) Argument {
	switch {
	case strings.HasSuffix(token, "?*"), strings.HasSuffix(token, "*?"):
		return Argument{Name: strings.TrimSuffix(strings.TrimSuffix(token, "?*"), "*?"), Description: description, Array: true}
	case strings.HasSuffix(token, "*"):
		return Argument{Name: strings.TrimSuffix(token, "*"), Description: description, Required: true, Array: true}
	case strings.HasSuffix(token, "?"):
		return Argument{Name: strings.TrimSuffix(token, "?"), Description: description}
	}

	if name, value, ok := strings.Cut(token, "=*"); ok {
		return Argument{Name: name, Description: description, Array: true, Default: splitDefaults(value)}
	}

	if name, value, ok := strings.Cut(token, "="); ok {
		return Argument{Name: name, Description: description, Default: []string{value}}
	}

	return Argument{Name: token, Description: description, Required: true}
}

// parseOption parses an option token without its `--`: `S|name`, then
// `name`, `name=`, `name=default`, `name=*` or `name=*a,b`.
func parseOption(token, description string) Option {
	option := Option{Description: description}

	if shortcut, rest, ok := strings.Cut(token, "|"); ok {
		option.Shortcut = shortcut
		token = rest
	}

	switch {
	case strings.HasSuffix(token, "=*"):
		option.Name = strings.TrimSuffix(token, "=*")
		option.AcceptsValue = true
		option.Array = true
	case strings.Contains(token, "=*"):
		name, value, _ := strings.Cut(token, "=*")
		option.Name = name
		option.AcceptsValue = true
		option.Array = true
		option.Default = splitDefaults(value)
	case strings.HasSuffix(token, "="):
		option.Name = strings.TrimSuffix(token, "=")
		option.AcceptsValue = true
	case strings.Contains(token, "="):
		name, value, _ := strings.Cut(token, "=")
		option.Name = name
		option.AcceptsValue = true
		option.Default = []string{value}
	default:
		option.Name = token
	}

	return option
}

// splitDefaults splits an array default list; empty means none.
func splitDefaults(value string) []string {
	if value == "" {
		return nil
	}

	return strings.Split(value, ",")
}

// option finds a declared option by name.
func (d Definition) option(name string) (Option, bool) {
	for _, option := range d.Options {
		if option.Name == name {
			return option, true
		}
	}

	return Option{}, false
}

// shortcut finds a declared option by its one-letter shortcut.
func (d Definition) shortcut(shortcut string) (Option, bool) {
	for _, option := range d.Options {
		if option.Shortcut == shortcut {
			return option, true
		}
	}

	return Option{}, false
}

// Usage is the one-line synopsis `help` prints: name, options, arguments.
func (d Definition) Usage() string {
	parts := []string{d.Name}
	if len(d.Options) > 0 {
		parts = append(parts, "[options]")
	}

	for _, argument := range d.Arguments {
		name := "<" + argument.Name + ">"
		if argument.Array {
			name += "..."
		}

		if !argument.Required {
			name = "[" + name + "]"
		}

		parts = append(parts, name)
	}

	return strings.Join(parts, " ")
}
