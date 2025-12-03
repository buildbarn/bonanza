package arguments

// BuildSettingOverride contains the label of a user-defined build
// setting for which an override was provided on the command line or in
// a bazelrc file, and the value that is assigned to it.
type BuildSettingOverride struct {
	Label string
	Value string
}

// Command denotes a specific subcommand of the Bazel command line tool
// for which arguments have been parsed.
type Command interface {
	Reset()
}

type commandAncestor struct {
	name      string
	mustApply bool
}

// ParseCommandAndArguments parses the name of a command like "build" or
// "test", and any of the arguments that follow that are specific to
// that command.
func ParseCommandAndArguments(configurationDirectives ConfigurationDirectives, args []string) (Command, error) {
	var cmd assignableCommand
	var ancestors []commandAncestor
	if len(args) == 0 {
		cmd = &HelpCommand{}
		ancestors = helpAncestors
	} else {
		var ok bool
		cmd, ancestors, ok = newCommandByName(args[0])
		if !ok {
			return nil, CommandNotRecognizedError{
				Command: args[0],
			}
		}
		args = args[1:]
	}
	cmd.Reset()

	return cmd, parseArguments(cmd, ancestors, configurationDirectives, args)
}

var boolExpectedValues = []string{
	"true",
	"false",
	"yes",
	"no",
	"1",
	"0",
}

type stackEntry struct {
	remainingArgs []string
	mustApply     bool
	allowFlags    bool
	directiveName string
}

func parseBool(hasValue bool, value string, out *bool, flagName string) error {
	v := true
	if hasValue {
		switch value {
		case "0", "false", "no":
			v = false
		case "1", "true", "yes":
			v = true
		default:
			return FlagInvalidEnumValueError{
				Flag:           flagName,
				Value:          value,
				ExpectedValues: boolExpectedValues,
			}
		}
	}
	if out != nil {
		*out = v
	}
	return nil
}
