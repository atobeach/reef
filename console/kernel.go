package console

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/atobeach/reef/panics"
)

// KernelConfig configures the console kernel.
type KernelConfig struct {
	// Name heads `list`.
	Name string
	// Report receives the errors commands Report and the panics they raise.
	Report func(ctx context.Context, err error)
}

// Kernel is the console kernel: the command registry the CLI dispatches to.
type Kernel struct {
	bind     func(ctx context.Context) context.Context
	config   KernelConfig
	commands map[string]registered
}

// registered is a command with its signature parsed once at Register.
type registered struct {
	command    Command
	definition Definition
}

// NewKernel builds an empty kernel. bind attaches the app's services (the
// database connection, the clock) to every command's context, as the HTTP
// kernel does per request; nil binds nothing.
func NewKernel(bind func(ctx context.Context) context.Context, config KernelConfig) *Kernel {
	return &Kernel{bind: bind, config: config, commands: map[string]registered{}}
}

// Register adds commands. A malformed signature or a duplicate name is a
// programming error and panics at boot, never at run time.
func (k *Kernel) Register(commands ...Command) {
	for _, command := range commands {
		definition, err := ParseSignature(command.Signature())
		if err != nil {
			panic(err)
		}

		if _, exists := k.commands[definition.Name]; exists {
			panic(fmt.Sprintf("console: command %q registered twice", definition.Name))
		}

		k.commands[definition.Name] = registered{command: command, definition: definition}
	}
}

// Has reports whether a command is registered.
func (k *Kernel) Has(name string) bool {
	_, ok := k.commands[name]

	return ok
}

// Names lists every registered command, sorted.
func (k *Kernel) Names() []string {
	names := make([]string, 0, len(k.commands))
	for name := range k.commands {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// Definition returns a registered command's parsed signature.
func (k *Kernel) Definition(name string) (Definition, bool) {
	command, ok := k.commands[name]

	return command.definition, ok
}

// Run resolves argv's command (or `list`/`help`), parses its input and runs
// it, returning the process exit code.
func (k *Kernel) Run(ctx context.Context, argv []string, w io.Writer) int {
	out := NewOutput(w)

	if len(argv) == 0 || argv[0] == "list" || argv[0] == "--help" || argv[0] == "-h" {
		namespace := ""
		if len(argv) > 1 && argv[0] == "list" {
			namespace = argv[1]
		}

		k.list(out, namespace)

		return Success
	}

	if argv[0] == "help" {
		if len(argv) < 2 {
			k.list(out, "")

			return Success
		}

		return k.help(out, argv[1])
	}

	if slices.Contains(argv[1:], "--help") || slices.Contains(argv[1:], "-h") {
		return k.help(out, argv[0])
	}

	return k.Call(ctx, argv[0], argv[1:], out)
}

// Call runs a command by name with already-split args and returns its exit
// code; the scheduler uses it. A panic is reported and printed, and fails
// the command.
func (k *Kernel) Call(ctx context.Context, name string, args []string, out *Output) (code int) {
	command, ok := k.commands[name]
	if !ok {
		out.Error(k.notDefined(name))

		return Failure
	}

	input, err := ParseInput(command.definition, args)
	if err != nil {
		out.Error(err.Error())
		out.Line("")
		out.Line(command.definition.Usage())

		return Failure
	}

	if input.Flag("quiet") {
		out = &Output{w: io.Discard}
	}

	if k.bind != nil {
		ctx = k.bind(ctx)
	}

	c := &Context{Context: ctx, Input: input, Output: out, kernel: k}

	defer func() {
		if recovered := recover(); recovered != nil {
			k.report(ctx, fmt.Errorf("console: %s panicked: %w", name, panics.New(recovered)))

			out.Errorf("%v", recovered)
			code = Failure
		}
	}()

	return exitCode(out, command.command.Handle(c))
}

// report hands err to the configured Report, if any.
func (k *Kernel) report(ctx context.Context, err error) {
	if k.config.Report != nil {
		k.config.Report(ctx, err)
	}
}

// notDefined is the unknown-command message, suggesting near names.
func (k *Kernel) notDefined(name string) string {
	message := `Command "` + name + `" is not defined.`

	var alternatives []string
	for _, candidate := range k.Names() {
		if strings.Contains(candidate, name) || levenshtein(candidate, name) <= len(name)/3+1 {
			alternatives = append(alternatives, candidate)
		}
	}

	if len(alternatives) > 0 {
		message += "\n\nDid you mean one of these?\n    " + strings.Join(alternatives, "\n    ")
	}

	return message
}

// list prints `list`: usage, then commands grouped by namespace.
func (k *Kernel) list(out *Output, namespace string) {
	out.Line(k.config.Name)
	out.NewLine()
	out.Comment("Usage:")
	out.Line("  command [options] [arguments]")
	out.NewLine()
	out.Comment("Options:")

	for _, option := range globalOptions {
		out.Line("  " + padRight(optionLabel(option), 22) + option.Description)
	}

	out.NewLine()

	if namespace != "" {
		out.Commentf("Available commands for the %q namespace:", namespace)
	} else {
		out.Comment("Available commands:")
	}

	names := append([]string{"help", "list"}, k.Names()...)
	sort.SliceStable(names, func(i, j int) bool {
		// Global (unnamespaced) commands first.
		iGlobal, jGlobal := !strings.Contains(names[i], ":"), !strings.Contains(names[j], ":")
		if iGlobal != jGlobal {
			return iGlobal
		}

		return names[i] < names[j]
	})

	width := 0
	for _, name := range names {
		width = max(width, len(name))
	}

	group := ""
	for _, name := range names {
		prefix, _, namespaced := strings.Cut(name, ":")
		if namespace != "" && (!namespaced || prefix != namespace) {
			continue
		}

		if namespaced && prefix != group {
			group = prefix
			out.Comment(" " + group)
		}

		out.Line("  " + padRight(name, width+2) + k.describe(name))
	}
}

// describe is a command's one-liner in `list`.
func (k *Kernel) describe(name string) string {
	switch name {
	case "help":
		return "Display help for a command"
	case "list":
		return "List commands"
	}

	return k.commands[name].command.Description()
}

// help prints `help <command>`: description, usage, arguments, options.
func (k *Kernel) help(out *Output, name string) int {
	command, ok := k.commands[name]
	if !ok {
		out.Error(k.notDefined(name))

		return Failure
	}

	definition := command.definition

	out.Comment("Description:")
	out.Line("  " + command.command.Description())
	out.NewLine()
	out.Comment("Usage:")
	out.Line("  " + definition.Usage())

	if len(definition.Arguments) > 0 {
		out.NewLine()
		out.Comment("Arguments:")

		for _, argument := range definition.Arguments {
			description := argument.Description
			if len(argument.Default) > 0 {
				description += fmt.Sprintf(" [default: %q]", strings.Join(argument.Default, ","))
			}

			out.Line("  " + padRight(argument.Name, 24) + description)
		}
	}

	out.NewLine()
	out.Comment("Options:")

	for _, option := range append(append([]Option(nil), definition.Options...), globalOptions...) {
		description := option.Description
		if len(option.Default) > 0 {
			description += fmt.Sprintf(" [default: %q]", strings.Join(option.Default, ","))
		}

		if option.Array {
			description += " (multiple values allowed)"
		}

		out.Line("  " + padRight(optionLabel(option), 24) + description)
	}

	return Success
}

// optionLabel is an option's left column in list and help: "-q, --quiet".
func optionLabel(option Option) string {
	label := "    --" + option.Name
	if option.Shortcut != "" {
		label = "-" + option.Shortcut + ", --" + option.Name
	}

	if option.AcceptsValue {
		label += "[=" + strings.ToUpper(option.Name) + "]"
	}

	return label
}

// padRight pads s to width, always leaving at least one space after it.
func padRight(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}

	return s + strings.Repeat(" ", width-len(s))
}

// levenshtein is the edit distance notDefined uses to suggest commands.
func levenshtein(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}

	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i

		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}

			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}

		previous = current
	}

	return previous[len(b)]
}
