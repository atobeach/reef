package console

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// globalOptions are accepted by every command. The kernel honours --help and
// --quiet; the rest are ignored, so muscle-memory flags like `-n` or `-v`
// still work.
var globalOptions = []Option{
	{Name: "help", Shortcut: "h", Description: "Display help for the given command"},
	{Name: "quiet", Shortcut: "q", Description: "Do not output any message"},
	{Name: "no-interaction", Shortcut: "n", Description: "Do not ask any interactive question"},
	{Name: "ansi", Description: "Force ANSI output"},
	{Name: "no-ansi", Description: "Disable ANSI output"},
	{Name: "verbose", Shortcut: "v", Description: "Increase the verbosity of messages"},
	{Name: "env", AcceptsValue: true, Description: "The environment the command should run under"},
}

// Input is a command's parsed arguments and options. Every accessor panics
// on a name the command's signature doesn't declare (the global options
// excepted), and a value read panics on a value-less option: the kernel
// reports the panic and fails the command.
type Input struct {
	definition Definition
	arguments  map[string][]string
	options    map[string][]string
	flags      map[string]bool
}

// ParseInput parses argv (without the command name) against the definition:
// `--opt=value`, `--opt value`, `-S value`, `-Svalue`, repeated array
// options, and `--` ending option parsing.
func ParseInput(definition Definition, argv []string) (*Input, error) {
	in := &Input{definition: definition, arguments: map[string][]string{}, options: map[string][]string{}, flags: map[string]bool{}}

	var positional []string
	optionsEnded := false

	for i := 0; i < len(argv); i++ {
		token := argv[i]

		switch {
		case optionsEnded || token == "-" || !strings.HasPrefix(token, "-"):
			positional = append(positional, token)
		case token == "--":
			optionsEnded = true
		case strings.HasPrefix(token, "--"):
			name, value, hasValue := strings.Cut(token[2:], "=")

			option, ok := in.lookup(name)
			if !ok {
				return nil, usageErrorf("The \"--%s\" option does not exist.", name)
			}

			consumed, err := in.set(option, value, hasValue, argv, i)
			if err != nil {
				return nil, err
			}

			i += consumed
		default:
			// -v / -vv / -vvv are verbosity levels.
			if strings.Trim(token[1:], "v") == "" {
				in.flags["verbose"] = true

				continue
			}

			shortcut := token[1:2]
			option, ok := in.lookupShortcut(shortcut)
			if !ok {
				return nil, usageErrorf("The \"-%s\" option does not exist.", shortcut)
			}

			rest := token[2:]
			consumed, err := in.set(option, rest, rest != "", argv, i)
			if err != nil {
				return nil, err
			}

			i += consumed
		}
	}

	if err := in.bindArguments(positional); err != nil {
		return nil, err
	}

	return in, nil
}

// lookup finds a long option, the command's own before the global ones.
func (in *Input) lookup(name string) (Option, bool) {
	if option, ok := in.definition.option(name); ok {
		return option, true
	}

	for _, option := range globalOptions {
		if option.Name == name {
			return option, true
		}
	}

	return Option{}, false
}

// lookupShortcut finds a one-letter option, the command's own first.
func (in *Input) lookupShortcut(shortcut string) (Option, bool) {
	if option, ok := in.definition.shortcut(shortcut); ok {
		return option, true
	}

	for _, option := range globalOptions {
		if option.Shortcut == shortcut {
			return option, true
		}
	}

	return Option{}, false
}

// set records one option occurrence and reports how many following argv
// tokens it consumed as its value.
func (in *Input) set(option Option, value string, hasValue bool, argv []string, at int) (int, error) {
	if !option.AcceptsValue {
		if hasValue {
			return 0, usageErrorf("The \"--%s\" option does not accept a value.", option.Name)
		}

		in.flags[option.Name] = true

		return 0, nil
	}

	consumed := 0
	if !hasValue && at+1 < len(argv) {
		// The next token is the value unless it looks like another option.
		if next := argv[at+1]; next == "" || next[0] != '-' {
			value = next
			hasValue = true
			consumed = 1
		}
	}

	if !hasValue {
		// `--output=` declared VALUE_OPTIONAL: present without a value is null.
		in.flags[option.Name] = true

		return consumed, nil
	}

	if option.Array {
		in.options[option.Name] = append(in.options[option.Name], value)
	} else {
		in.options[option.Name] = []string{value}
	}

	in.flags[option.Name] = true

	return consumed, nil
}

// bindArguments assigns positional values to the declared arguments in
// order, an array argument taking the rest.
func (in *Input) bindArguments(positional []string) error {
	arguments := in.definition.Arguments

	for i, argument := range arguments {
		if i >= len(positional) {
			break
		}

		if argument.Array {
			in.arguments[argument.Name] = append([]string(nil), positional[i:]...)
			positional = positional[:i]

			break
		}

		in.arguments[argument.Name] = []string{positional[i]}
	}

	if len(positional) > len(arguments) && (len(arguments) == 0 || !arguments[len(arguments)-1].Array) {
		extra := positional[len(arguments)]
		if len(arguments) == 0 {
			return usageErrorf("No arguments expected for \"%s\" command, got \"%s\".", in.definition.Name, extra)
		}

		names := make([]string, len(arguments))
		for i, argument := range arguments {
			names[i] = `"` + argument.Name + `"`
		}

		return usageErrorf("Too many arguments, expected arguments %s.", strings.Join(names, " "))
	}

	var missing []string
	for _, argument := range arguments {
		if _, given := in.arguments[argument.Name]; argument.Required && !given {
			missing = append(missing, argument.Name)
		}
	}

	if len(missing) > 0 {
		return usageErrorf("Not enough arguments (missing: \"%s\").", strings.Join(missing, ", "))
	}

	return nil
}

// argument is the declared argument called name. A name the signature never
// declares is a typo that would read as empty forever, so it panics: the
// kernel reports the panic and fails the command.
func (in *Input) argument(name string) Argument {
	for _, argument := range in.definition.Arguments {
		if argument.Name == name {
			return argument
		}
	}

	panic(fmt.Sprintf("console: %s declares no argument %q", in.definition.Name, name))
}

// option is the declared (or global) option called name, panicking likewise.
func (in *Input) option(name string) Option {
	option, ok := in.lookup(name)
	if !ok {
		panic(fmt.Sprintf("console: %s declares no option \"--%s\"", in.definition.Name, name))
	}

	return option
}

// valueOption is option for a value read: a flag never carries one.
func (in *Input) valueOption(name string) Option {
	option := in.option(name)
	if !option.AcceptsValue {
		panic(fmt.Sprintf("console: %s's \"--%s\" takes no value; read it with Flag", in.definition.Name, name))
	}

	return option
}

// Argument is an argument's value, its default, or "".
func (in *Input) Argument(name string) string {
	values := in.ArgumentList(name)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

// HasArgument reports whether the argument was given rather than defaulted,
// which tells an omitted optional argument from an empty one.
func (in *Input) HasArgument(name string) bool {
	in.argument(name)
	_, ok := in.arguments[name]

	return ok
}

// ArgumentList is an array argument's values.
func (in *Input) ArgumentList(name string) []string {
	argument := in.argument(name)
	if values, ok := in.arguments[name]; ok {
		return values
	}

	return argument.Default
}

// Option is a value option's value (the last one given), its default, or "".
func (in *Input) Option(name string) string {
	values := in.OptionList(name)
	if len(values) == 0 {
		return ""
	}

	return values[len(values)-1]
}

// HasOption reports whether a value option was given at all.
func (in *Input) HasOption(name string) bool {
	in.option(name)

	return in.flags[name]
}

// OptionList is an array option's values (`--model=A --model=B`).
func (in *Input) OptionList(name string) []string {
	option := in.valueOption(name)
	if values, ok := in.options[name]; ok {
		return values
	}

	return option.Default
}

// IntOption is a whole-number option's value, at least minimum; anything
// else is a usage error naming the option.
func (in *Input) IntOption(name string, minimum int) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(in.Option(name)))
	if err != nil || value < minimum {
		return 0, usageErrorf("The \"--%s\" option must be a whole number of at least %d.", name, minimum)
	}

	return value, nil
}

// SecondsOption is an option given in whole seconds (`--timeout=60`) as a
// duration; a negative or non-numeric value is a usage error naming the option.
func (in *Input) SecondsOption(name string) (time.Duration, error) {
	seconds, err := strconv.Atoi(strings.TrimSpace(in.Option(name)))
	if err != nil || seconds < 0 {
		return 0, usageErrorf("The \"--%s\" option must be a whole number of seconds.", name)
	}

	return time.Duration(seconds) * time.Second, nil
}

// Flag reports whether a value-less option (`--force`) was given.
func (in *Input) Flag(name string) bool {
	in.option(name)

	return in.flags[name]
}

// usageError is a command-line mistake, printed to the user verbatim as a
// full sentence — which is why it isn't a lower-case Go error string.
type usageError string

func (e usageError) Error() string { return string(e) }

func usageErrorf(format string, args ...any) error {
	return usageError(fmt.Sprintf(format, args...))
}
