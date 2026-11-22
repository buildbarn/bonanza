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
//
// The platform name is the name of the host operating system (e.g.,
// "linux", "macos"). If --enable_platform_specific_config is set, it
// is used to apply --config=${platform_name} implicitly.
func ParseCommandAndArguments(configurationDirectives ConfigurationDirectives, platformName string, args []string) (Command, error) {
	commandName := "help"
	if len(args) > 0 {
		commandName = args[0]
		args = args[1:]
	}
	newCommand := func() (assignableCommand, []commandAncestor, error) {
		cmd, ancestors, ok := newCommandByName(commandName)
		if !ok {
			return nil, nil, CommandNotRecognizedError{
				Command: commandName,
			}
		}
		cmd.Reset()
		return cmd, ancestors, nil
	}

	cmd, ancestors, err := newCommand()
	if err != nil {
		return nil, err
	}
	if err := parseArguments(cmd, ancestors, configurationDirectives, args, nil); err != nil {
		return nil, err
	}

	// Whether --enable_platform_specific_config is set is only
	// known after all arguments have been parsed, as it may be
	// provided through bazelrc files, the command line, or other
	// --config flags. If set, parse all arguments once more, this
	// time expanding --config=${platform_name} right before the
	// arguments provided on the command line. This causes it to
	// take precedence over bazelrc files, but not over explicitly
	// provided command line flags.
	//
	// Unlike explicitly provided --config flags, it is not an error
	// if no directives for the platform exist.
	if cmd.getCommonFlags().EnablePlatformSpecificConfig &&
		platformName != "" &&
		hasConfigurationDirectives(configurationDirectives, ancestors, platformName) {
		cmd, ancestors, err = newCommand()
		if err != nil {
			return nil, err
		}
		if err := parseArguments(cmd, ancestors, configurationDirectives, args, []string{"--config=" + platformName}); err != nil {
			return nil, err
		}
	}
	return cmd, nil
}

func hasConfigurationDirectives(configurationDirectives ConfigurationDirectives, ancestors []commandAncestor, config string) bool {
	for _, ancestor := range ancestors {
		if _, ok := configurationDirectives[ancestor.name+":"+config]; ok {
			return true
		}
	}
	return false
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
