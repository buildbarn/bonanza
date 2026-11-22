package main

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

type flag struct {
	longName    string
	shortName   string
	description string
	flagType    flagType
}

type command struct {
	ancestor       string
	flags          []flag
	takesArguments bool
}

func toSymbolName(s string, public bool) string {
	fields := strings.FieldsFunc(s, func(c rune) bool {
		return (c < '0' || c > '9') && (c < 'a' || c > 'z')
	})
	start := 1
	if public {
		start = 0
	}
	for i := start; i < len(fields); i++ {
		fields[i] = strings.Title(fields[i])
	}
	return strings.Join(fields, "")
}

func main() {
	fmt.Println("package arguments")
	fmt.Println("import (")
	fmt.Println("\"strings\"")
	fmt.Println(")")

	for _, enumTypeName := range slices.Sorted(maps.Keys(enumTypes)) {
		fmt.Printf("type %s int\n", enumTypeName)
		enumValueNames := enumTypes[enumTypeName]
		sort.Strings(enumValueNames)
		for i, enumValueName := range enumValueNames {
			fmt.Printf("const %s_%s = %d\n", enumTypeName, toSymbolName(enumValueName, true), i+1)
		}
		fmt.Printf("var expectedValues_%s = %#v\n", enumTypeName, enumValueNames)
		fmt.Printf("func (v *%s) set(flagName string, valueString string) error {\n", enumTypeName)
		fmt.Printf("  var newV %s\n", enumTypeName)
		fmt.Printf("  switch valueString {\n")
		for _, enumValueName := range enumValueNames {
			fmt.Printf("  case %#v:\n", enumValueName)
			fmt.Printf("    newV = %s_%s\n", enumTypeName, toSymbolName(enumValueName, true))
		}
		fmt.Printf("  default:\n")
		fmt.Printf("    return FlagInvalidEnumValueError{\n")
		fmt.Printf("      Flag: flagName,\n")
		fmt.Printf("      Value: valueString,\n")
		fmt.Printf("      ExpectedValues: expectedValues_%s,\n", enumTypeName)
		fmt.Printf("    }\n")
		fmt.Printf("  }\n")
		fmt.Printf("  if v != nil {\n")
		fmt.Printf("    *v = newV\n")
		fmt.Printf("  }\n")
		fmt.Printf("  return nil\n")
		fmt.Printf("}\n")
	}

	allFlags := map[string][]flag{
		"startup": startupFlags,
		"common":  commonFlags,
	}
	commandFlags := map[string][]flag{
		"common": commonFlags,
	}
	for commandName, command := range commands {
		allFlags[commandName] = command.flags
		commandFlags[commandName] = command.flags
	}

	for _, flagsName := range slices.Sorted(maps.Keys(allFlags)) {
		flagsSymbolName := toSymbolName(flagsName, true)
		fmt.Printf("type %sFlags struct{\n", flagsSymbolName)
		for _, flag := range allFlags[flagsName] {
			flag.flagType.emitStructField(flag.longName)
		}
		fmt.Printf("}\n")

		fmt.Printf("func (f *%sFlags) Reset() {\n", flagsSymbolName)
		for _, flag := range allFlags[flagsName] {
			flag.flagType.emitDefaultInitializer(flag.longName)
		}
		fmt.Printf("}\n")
	}

	for _, commandName := range slices.Sorted(maps.Keys(commands)) {
		fmt.Printf("var %sAncestors = []commandAncestor{\n", toSymbolName(commandName, false))
		for ancestorName := commandName; ancestorName != "common"; ancestorName = commands[ancestorName].ancestor {
			fmt.Printf("{name: %#v, mustApply: true},\n", ancestorName)
		}
		fmt.Printf("{name: \"common\", mustApply: false},\n")
		fmt.Printf("{name: \"always\", mustApply: true},\n")
		fmt.Printf("}\n")
	}

	fmt.Printf("type assignableCommand interface {\n")
	fmt.Printf("  Command\n")
	for _, flagsName := range slices.Sorted(maps.Keys(commandFlags)) {
		flagSetSymbolName := toSymbolName(flagsName, true)
		fmt.Printf("  get%sFlags() *%sFlags\n", flagSetSymbolName, flagSetSymbolName)
	}
	fmt.Printf("  appendArgument(argument string) error\n")
	fmt.Printf("  appendBuildSettingOverride(override BuildSettingOverride)\n")
	fmt.Printf("}\n")

	for _, commandName := range slices.Sorted(maps.Keys(commands)) {
		allAncestors := map[string]struct{}{}
		for ancestorName := commandName; ; ancestorName = commands[ancestorName].ancestor {
			allAncestors[ancestorName] = struct{}{}
			if ancestorName == "common" {
				break
			}
		}

		command := commands[commandName]
		commandSymbolName := toSymbolName(commandName, true)
		fmt.Printf("type %sCommand struct {\n", commandSymbolName)
		for _, ancestorName := range slices.Sorted(maps.Keys(allAncestors)) {
			flagSetSymbolName := toSymbolName(ancestorName, true)
			fmt.Printf("%sFlags %sFlags\n", flagSetSymbolName, flagSetSymbolName)
		}
		if command.takesArguments {
			fmt.Printf("Arguments []string\n")
		}
		fmt.Printf("BuildSettingOverrides []BuildSettingOverride\n")
		fmt.Printf("}\n")

		fmt.Printf("func (c *%sCommand) Reset() {\n", commandSymbolName)
		for _, ancestorName := range slices.Sorted(maps.Keys(allAncestors)) {
			fmt.Printf("c.%sFlags.Reset()\n", toSymbolName(ancestorName, true))
		}
		fmt.Printf("}\n")

		for _, flagsName := range slices.Sorted(maps.Keys(commandFlags)) {
			flagsSymbolName := toSymbolName(flagsName, true)
			fmt.Printf("func (c *%sCommand) get%sFlags() *%sFlags {\n", commandSymbolName, flagsSymbolName, flagsSymbolName)
			if _, ok := allAncestors[flagsName]; ok {
				fmt.Printf("  return &c.%sFlags", flagsSymbolName)
			} else {
				fmt.Printf("  return nil")
			}
			fmt.Printf("}\n")
		}

		fmt.Printf("func (c *%sCommand) appendArgument(argument string) error {\n", commandSymbolName)
		if command.takesArguments {
			fmt.Printf("  c.Arguments = append(c.Arguments, argument)\n")
			fmt.Printf("  return nil\n")
		} else {
			fmt.Printf("  return CommandDoesNotTakeArgumentsError{Command: %#v}\n", commandName)
		}
		fmt.Printf("}\n")

		fmt.Printf("func (c *%sCommand) appendBuildSettingOverride(override BuildSettingOverride) {\n", commandSymbolName)
		fmt.Printf("  c.BuildSettingOverrides = append(c.BuildSettingOverrides, override)\n")
		fmt.Printf("}\n")
	}

	fmt.Printf("func newCommandByName(name string) (assignableCommand, []commandAncestor, bool) {\n")
	fmt.Printf("switch name {")
	for _, commandName := range slices.Sorted(maps.Keys(commands)) {
		fmt.Printf("case %#v:\n", commandName)
		fmt.Printf("return &%sCommand{}, %sAncestors, true\n", toSymbolName(commandName, true), toSymbolName(commandName, false))
	}
	fmt.Printf("default:\n")
	fmt.Printf("return nil, nil, false\n")
	fmt.Printf("}\n")
	fmt.Printf("}\n")

	fmt.Printf("func parseArguments(cmd assignableCommand, ancestors []commandAncestor, configurationDirectives ConfigurationDirectives, args, platformSpecificArgs []string) error {\n")
	fmt.Printf("  stack := []stackEntry{{\n")
	fmt.Printf("    remainingArgs: args,\n")
	fmt.Printf("    mustApply: true,\n")
	fmt.Printf("  }}\n")
	fmt.Printf("  if len(platformSpecificArgs) > 0 {\n")
	fmt.Printf("    stack = append(stack, stackEntry{\n")
	fmt.Printf("      remainingArgs: platformSpecificArgs,\n")
	fmt.Printf("      mustApply: true,\n")
	fmt.Printf("    })\n")
	fmt.Printf("  }\n")
	fmt.Printf("  for _, ancestor := range ancestors {\n")
	fmt.Printf("    directives := configurationDirectives[ancestor.name]\n")
	fmt.Printf("    for i := len(directives)-1; i >= 0; i-- {\n")
	fmt.Printf("      stack = append(stack, stackEntry{\n")
	fmt.Printf("        remainingArgs: directives[i],\n")
	fmt.Printf("        mustApply: ancestor.mustApply,\n")
	fmt.Printf("      })\n")
	fmt.Printf("    }\n")
	fmt.Printf("  }\n")
	fmt.Printf("  allowFlags := true\n")
	fmt.Printf("  for len(stack) > 0 {\n")
	fmt.Printf("    currentStackEntry := &stack[len(stack)-1]\n")
	fmt.Printf("    currentArgs := &currentStackEntry.remainingArgs\n")
	fmt.Printf("    if len(*currentArgs) == 0 {\n")
	fmt.Printf("      stack = stack[:len(stack)-1]\n")
	fmt.Printf("      allowFlags = true\n")
	fmt.Printf("    } else {\n")
	fmt.Printf("      mustApply := currentStackEntry.mustApply\n")
	fmt.Printf("      firstArg := (*currentArgs)[0]\n")
	fmt.Printf("      (*currentArgs) = (*currentArgs)[1:]\n")
	fmt.Printf("      if allowFlags && len(firstArg) > 1 && firstArg[0] == '-' && firstArg[1] == '-' {\n")
	fmt.Printf("        longOptionName := firstArg\n")
	fmt.Printf("        var optionValue string\n")
	fmt.Printf("        assignmentIndex := strings.IndexByte(longOptionName, '=')\n")
	fmt.Printf("        if assignmentIndex >= 0 {\n")
	fmt.Printf("          optionValue = longOptionName[assignmentIndex+1:]\n")
	fmt.Printf("          longOptionName = longOptionName[:assignmentIndex]\n")
	fmt.Printf("        }\n")
	fmt.Printf("        switch longOptionName {\n")
	for _, flagsName := range slices.Sorted(maps.Keys(commandFlags)) {
		for _, flag := range commandFlags[flagsName] {
			flag.flagType.emitLongNameParser(flagsName, flag.longName)
		}
	}
	fmt.Printf("        case \"--config\":\n")
	fmt.Printf("          if assignmentIndex < 0 {\n")
	fmt.Printf("            if len(*currentArgs) == 0 {\n")
	fmt.Printf("              return FlagMissingValueError{Flag: longOptionName}\n")
	fmt.Printf("            }\n")
	fmt.Printf("            optionValue = (*currentArgs)[0]\n")
	fmt.Printf("            (*currentArgs) = (*currentArgs)[1:]\n")
	fmt.Printf("          }\n")
	fmt.Printf("          foundDirectives := false\n")
	fmt.Printf("          for _, ancestor := range ancestors {\n")
	fmt.Printf("            directiveName := ancestor.name + \":\" + optionValue\n")
	fmt.Printf("            for _, entry := range stack {\n")
	fmt.Printf("              if entry.directiveName == directiveName {\n")
	fmt.Printf("                return ConfigExpansionContainsCycleError{Directive: directiveName}\n")
	fmt.Printf("              }\n")
	fmt.Printf("            }\n")
	fmt.Printf("            directives, ok := configurationDirectives[directiveName]\n")
	fmt.Printf("            if ok {\n")
	fmt.Printf("              foundDirectives = true\n")
	fmt.Printf("              for i := len(directives)-1; i >= 0; i-- {\n")
	fmt.Printf("                stack = append(stack, stackEntry{\n")
	fmt.Printf("                  remainingArgs: directives[i],\n")
	fmt.Printf("                  mustApply: ancestor.mustApply,\n")
	fmt.Printf("                  directiveName: directiveName,\n")
	fmt.Printf("                })\n")
	fmt.Printf("              }\n")
	fmt.Printf("            }\n")
	fmt.Printf("          }\n")
	fmt.Printf("          if !foundDirectives {\n")
	fmt.Printf("            return ConfigValueNotRecognizedError{Config: optionValue}\n")
	fmt.Printf("          }\n")
	fmt.Printf("        case \"--\":\n")
	fmt.Printf("          allowFlags = false\n")
	fmt.Printf("        default:\n")
	fmt.Printf("          return FlagNotRecognizedError{Flag: longOptionName}\n")
	fmt.Printf("        }\n")
	fmt.Printf("      } else if allowFlags && len(firstArg) > 0 && firstArg[0] == '-' {\n")
	fmt.Printf("        shortOptionName := firstArg\n")
	fmt.Printf("        switch shortOptionName {\n")
	for _, flagsName := range slices.Sorted(maps.Keys(commandFlags)) {
		for _, flag := range commandFlags[flagsName] {
			if flag.shortName != "" {
				flag.flagType.emitShortNameParser(flagsName, flag.longName, flag.shortName)
			}
		}
	}
	fmt.Printf("        default:\n")
	fmt.Printf("          return FlagNotRecognizedError{Flag: shortOptionName}\n")
	fmt.Printf("        }\n")
	fmt.Printf("      } else {\n")
	fmt.Printf("        if err := cmd.appendArgument(firstArg); err != nil {\n")
	fmt.Printf("          return err\n")
	fmt.Printf("        }\n")
	fmt.Printf("      }\n")
	fmt.Printf("    }\n")
	fmt.Printf("  }\n")
	fmt.Printf("  return nil\n")
	fmt.Printf("}\n")

	fmt.Printf("func ParseStartupFlags(args []string) (*StartupFlags, int, error) {\n")
	fmt.Printf("  var flags StartupFlags\n")
	fmt.Printf("  flags.Reset()\n")
	fmt.Printf("  argsIndex := 0\n")
	fmt.Printf("  for argsIndex < len(args) {\n")
	fmt.Printf("    firstArg := args[argsIndex]\n")
	fmt.Printf("    if len(firstArg) < 2 || firstArg[0] != '-' || firstArg[1] != '-' {\n")
	fmt.Printf("      break\n")
	fmt.Printf("    }\n")
	fmt.Printf("    argsIndex++\n")
	fmt.Printf("    longOptionName := firstArg\n")
	fmt.Printf("    var optionValue string\n")
	fmt.Printf("    assignmentIndex := strings.IndexByte(longOptionName, '=')\n")
	fmt.Printf("    if assignmentIndex >= 0 {\n")
	fmt.Printf("      optionValue = longOptionName[assignmentIndex+1:]\n")
	fmt.Printf("      longOptionName = longOptionName[:assignmentIndex]\n")
	fmt.Printf("    }\n")
	fmt.Printf("    switch longOptionName {\n")
	for _, flag := range startupFlags {
		flag.flagType.emitStartupParser(flag.longName)
	}
	fmt.Printf("    default:\n")
	fmt.Printf("      return nil, 0, FlagNotRecognizedError{Flag: longOptionName}\n")
	fmt.Printf("    }\n")
	fmt.Printf("  }\n")
	fmt.Printf("  return &flags, argsIndex, nil\n")
	fmt.Printf("}\n")
}
