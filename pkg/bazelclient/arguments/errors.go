package arguments

import (
	"fmt"
	"strings"
)

// CommandNotRecognizedError is returned when the user invokes the CLI
// with an unknown command name (e.g., "bazel builf").
type CommandNotRecognizedError struct {
	Command string
}

func (e CommandNotRecognizedError) Error() string {
	return fmt.Sprintf("command %#v not recognized", e.Command)
}

// CommandDoesNotTakeArgumentsError is returned when the user invokes a
// command with arguments, even though the command does not take any
// (e.g., "bazel clean //...").
type CommandDoesNotTakeArgumentsError struct {
	Command string
}

func (e CommandDoesNotTakeArgumentsError) Error() string {
	return fmt.Sprintf("command %#v does not take arguments", e.Command)
}

// FlagNotRecognizedError is returned when the user invokes a command
// with an unknown flag (e.g., "bazel build --remote_executar=foo").
type FlagNotRecognizedError struct {
	Flag string
}

func (e FlagNotRecognizedError) Error() string {
	return fmt.Sprintf("flag %s not recognized", e.Flag)
}

// FlagNotApplicableError is returned when the user invokes a command
// with a flag that is known to exist, but does not pertain to that
// command (e.g., "bazel help --remote_executor=foo").
type FlagNotApplicableError struct {
	Flag string
}

func (e FlagNotApplicableError) Error() string {
	return fmt.Sprintf("flag %s does not apply to this command", e.Flag)
}

// FlagMissingValueError is returned when the user invokes a command
// with a flag that must take a value, but no value is provided (e.g.,
// "bazel build --remote_executor").
type FlagMissingValueError struct {
	Flag string
}

func (e FlagMissingValueError) Error() string {
	return fmt.Sprintf("flag %s expects a value", e.Flag)
}

// FlagUnexpectedValueError is returned when the user invokes a command
// with a flag that does not take a value, even though a value is
// provided (e.g., "bazel build --keep_going=42").
type FlagUnexpectedValueError struct {
	Flag string
}

func (e FlagUnexpectedValueError) Error() string {
	return fmt.Sprintf("flag %s does not take a value", e.Flag)
}

// FlagInvalidEnumValueError is returned when the user invokes a command
// with a flag that only accepts certain string values, but the value
// that is provided is not in the accepted set (e.g.
// "bazel build --compilation_mode=slowbuild").
type FlagInvalidEnumValueError struct {
	Flag           string
	Value          string
	ExpectedValues []string
}

func (e FlagInvalidEnumValueError) Error() string {
	var sb strings.Builder
	sb.WriteString("flag ")
	sb.WriteString(e.Flag)
	sb.WriteString(" only accepts ")
	for i, expectedValue := range e.ExpectedValues {
		fmt.Fprintf(&sb, "%#v", expectedValue)
		switch i {
		default:
			sb.WriteString(", ")
		case len(e.ExpectedValues) - 2:
			sb.WriteString(" or ")
		case len(e.ExpectedValues) - 1:
		}
	}
	sb.WriteString(", not ")
	fmt.Fprintf(&sb, "%#v", e.Value)
	return sb.String()
}

// ConfigValueNotRecognizedError is returned when the user provides the
// --config flag with a configuration name that is not declared in any
// of the bazelrc files.
type ConfigValueNotRecognizedError struct {
	Config string
}

func (e ConfigValueNotRecognizedError) Error() string {
	return fmt.Sprintf("config value %#v is not defined in any configuration file", e.Config)
}

// ConfigExpansionContainsCycleError is returned when the bazelrc files
// contain declarations with --config flags that refer to each other in
// cycles.
type ConfigExpansionContainsCycleError struct {
	Directive string
}

func (e ConfigExpansionContainsCycleError) Error() string {
	return fmt.Sprintf("config expansion for configuration directive %#v contains a cycle", e.Directive)
}
