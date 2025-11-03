package main

import (
	"fmt"
)

type flagType interface {
	emitStructField(longName string)
	emitDefaultInitializer(longName string)
	emitLongNameParser(flagSetName, longName string)
	emitShortNameParser(flagSetName, longName, shortName string)
	emitStartupParser(longName string)
}

type boolFlagType struct {
	defaultValue bool
}

func (boolFlagType) emitStructField(longName string) {
	fmt.Printf("%s bool\n", toSymbolName(longName, true))
}

func (ft boolFlagType) emitDefaultInitializer(longName string) {
	fmt.Printf("f.%s = %#v\n", toSymbolName(longName, true), ft.defaultValue)
}

func (boolFlagType) emitLongNameParser(flagSetName, longName string) {
	flagSetSymbolName := toSymbolName(flagSetName, true)
	longSymbolName := toSymbolName(longName, true)

	fmt.Printf("case %#v:\n", "--"+longName)
	fmt.Printf("  var out *bool\n")
	fmt.Printf("  if flags := cmd.get%sFlags(); flags != nil {\n", flagSetSymbolName)
	fmt.Printf("    out = &flags.%s\n", longSymbolName)
	fmt.Printf("  } else if mustApply {")
	fmt.Printf("    return FlagNotApplicableError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if err := parseBool(assignmentIndex >= 0, optionValue, out, longOptionName); err != nil {\n")
	fmt.Printf("    return err\n")
	fmt.Printf("  }\n")

	fmt.Printf("case %#v:\n", "--no"+longName)
	fmt.Printf("  var out *bool\n")
	fmt.Printf("  if flags := cmd.get%sFlags(); flags != nil {\n", flagSetSymbolName)
	fmt.Printf("    out = &flags.%s\n", longSymbolName)
	fmt.Printf("  } else if mustApply {")
	fmt.Printf("    return FlagNotApplicableError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if assignmentIndex >= 0 {\n")
	fmt.Printf("    return FlagUnexpectedValueError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if out != nil {\n")
	fmt.Printf("    *out = false\n")
	fmt.Printf("  }\n")
}

func (boolFlagType) emitShortNameParser(flagSetName, longName, shortName string) {
	flagSetSymbolName := toSymbolName(flagSetName, true)
	longSymbolName := toSymbolName(longName, true)

	fmt.Printf("case %#v:\n", "-"+shortName)
	fmt.Printf("  if flags := cmd.get%sFlags(); flags != nil {\n", flagSetSymbolName)
	fmt.Printf("    flags.%s = true\n", longSymbolName)
	fmt.Printf("  } else if mustApply {")
	fmt.Printf("    return FlagNotApplicableError{Flag: shortOptionName}\n")
	fmt.Printf("  }\n")

	fmt.Printf("case %#v:\n", "-"+shortName+"-")
	fmt.Printf("  if flags := cmd.get%sFlags(); flags != nil {\n", flagSetSymbolName)
	fmt.Printf("    flags.%s = false\n", longSymbolName)
	fmt.Printf("  } else if mustApply {")
	fmt.Printf("    return FlagNotApplicableError{Flag: shortOptionName}\n")
	fmt.Printf("  }\n")
}

func (boolFlagType) emitStartupParser(longName string) {
	longSymbolName := toSymbolName(longName, true)

	fmt.Printf("case %#v:\n", "--"+longName)
	fmt.Printf("  if err := parseBool(assignmentIndex >= 0, optionValue, &flags.%s, longOptionName); err != nil {\n", longSymbolName)
	fmt.Printf("    return nil, 0, err\n")
	fmt.Printf("  }\n")

	fmt.Printf("case %#v:\n", "--no"+longName)
	fmt.Printf("  if assignmentIndex >= 0 {\n")
	fmt.Printf("    return nil, 0, FlagUnexpectedValueError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  flags.%s = false\n", longSymbolName)
}

type buildSettingFlagType struct{}

func (buildSettingFlagType) emitStructField(longName string) {}

func (buildSettingFlagType) emitDefaultInitializer(longName string) {}

func (buildSettingFlagType) emitLongNameParser(flagSetName, longName string) {
	fmt.Printf("case %#v:\n", "--"+longName)
	fmt.Printf("  shouldApply := false\n")
	fmt.Printf("  if cmd.get%sFlags() != nil {\n", toSymbolName(flagSetName, true))
	fmt.Printf("    shouldApply = true\n")
	fmt.Printf("  } else if mustApply {\n")
	fmt.Printf("    return FlagNotApplicableError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if assignmentIndex < 0 {\n")
	fmt.Printf("    if len(*currentArgs) == 0 {\n")
	fmt.Printf("      return FlagMissingValueError{Flag: longOptionName}\n")
	fmt.Printf("    }\n")
	fmt.Printf("    optionValue = (*currentArgs)[0]\n")
	fmt.Printf("    (*currentArgs) = (*currentArgs)[1:]\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if shouldApply {\n")
	fmt.Printf("    cmd.appendBuildSettingOverride(BuildSettingOverride{\n")
	fmt.Printf("      Label: %#v,\n", "@bazel_tools//command_line_option:"+longName)
	fmt.Printf("      Value: optionValue,\n")
	fmt.Printf("    })\n")
	fmt.Printf("  }\n")
}

func (buildSettingFlagType) emitShortNameParser(flagSetName, longName, shortName string) {
	panic("build setting flags cannot be used with short names")
}

func (buildSettingFlagType) emitStartupParser(longName string) {
	panic("build setting flags cannot be used for startup flags")
}

type enumFlagType struct {
	enumType     string
	defaultValue string
}

func (ft enumFlagType) emitStructField(longName string) {
	fmt.Printf("%s %s\n", toSymbolName(longName, true), ft.enumType)
}

func (ft enumFlagType) emitDefaultInitializer(longName string) {
	fmt.Printf("f.%s = %s_%s\n", toSymbolName(longName, true), ft.enumType, toSymbolName(ft.defaultValue, true))
}

func (ft enumFlagType) emitLongNameParser(flagSetName, longName string) {
	fmt.Printf("case %#v:\n", "--"+longName)
	fmt.Printf("  var out *%s\n", ft.enumType)
	fmt.Printf("  if flags := cmd.get%sFlags(); flags != nil {\n", toSymbolName(flagSetName, true))
	fmt.Printf("    out = &flags.%s\n", toSymbolName(longName, true))
	fmt.Printf("  } else if mustApply {\n")
	fmt.Printf("    return FlagNotApplicableError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if assignmentIndex < 0 {\n")
	fmt.Printf("    if len(*currentArgs) == 0 {\n")
	fmt.Printf("      return FlagMissingValueError{Flag: longOptionName}\n")
	fmt.Printf("    }\n")
	fmt.Printf("    optionValue = (*currentArgs)[0]\n")
	fmt.Printf("    (*currentArgs) = (*currentArgs)[1:]\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if err := out.set(longOptionName, optionValue); err != nil {\n")
	fmt.Printf("    return err\n")
	fmt.Printf("  }\n")
}

func (enumFlagType) emitShortNameParser(flagSetName, longName, shortName string) {
	panic("TODO")
}

func (enumFlagType) emitStartupParser(longName string) {
	panic("TODO")
}

type expansionFlagType struct {
	expandsTo []string
}

func (expansionFlagType) emitStructField(longName string) {}

func (expansionFlagType) emitDefaultInitializer(longName string) {}

func (ft expansionFlagType) emitLongNameParser(flagSetName, longName string) {
	fmt.Printf("case %#v:\n", "--"+longName)
	fmt.Printf("  if mustApply && cmd.get%sFlags() == nil {\n", toSymbolName(flagSetName, true))
	fmt.Printf("    return FlagNotApplicableError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if assignmentIndex >= 0 {\n")
	fmt.Printf("    return FlagUnexpectedValueError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if mustApply {\n")
	fmt.Printf("    stack = append(stack, stackEntry{\n")
	fmt.Printf("      remainingArgs: %#v,\n", ft.expandsTo)
	fmt.Printf("      mustApply: true,\n")
	fmt.Printf("    })\n")
	fmt.Printf("  }\n")
}

func (ft expansionFlagType) emitShortNameParser(flagSetName, longName, shortName string) {
	fmt.Printf("case %#v:\n", "-"+shortName)
	fmt.Printf("  if mustApply && cmd.get%sFlags() == nil {\n", toSymbolName(flagSetName, true))
	fmt.Printf("    return FlagNotApplicableError{Flag: shortOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if mustApply {\n")
	fmt.Printf("    stack = append(stack, stackEntry{\n")
	fmt.Printf("      remainingArgs: %#v,\n", ft.expandsTo)
	fmt.Printf("      mustApply: true,\n")
	fmt.Printf("    })\n")
	fmt.Printf("  }\n")
}

func (expansionFlagType) emitStartupParser(longName string) {
	panic("TODO")
}

type stringFlagType struct {
	defaultValue string
}

func (stringFlagType) emitStructField(longName string) {
	fmt.Printf("%s string\n", toSymbolName(longName, true))
}

func (ft stringFlagType) emitDefaultInitializer(longName string) {
	fmt.Printf("f.%s = %#v\n", toSymbolName(longName, true), ft.defaultValue)
}

func (stringFlagType) emitLongNameParser(flagSetName, longName string) {
	fmt.Printf("case %#v:\n", "--"+longName)
	fmt.Printf("  var out *string\n")
	fmt.Printf("  if flags := cmd.get%sFlags(); flags != nil {\n", toSymbolName(flagSetName, true))
	fmt.Printf("    out = &flags.%s\n", toSymbolName(longName, true))
	fmt.Printf("  } else if mustApply {\n")
	fmt.Printf("    return FlagNotApplicableError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if assignmentIndex < 0 {\n")
	fmt.Printf("    if len(*currentArgs) == 0 {\n")
	fmt.Printf("      return FlagMissingValueError{Flag: longOptionName}\n")
	fmt.Printf("    }\n")
	fmt.Printf("    optionValue = (*currentArgs)[0]\n")
	fmt.Printf("    (*currentArgs) = (*currentArgs)[1:]\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if out != nil {\n")
	fmt.Printf("    *out = optionValue\n")
	fmt.Printf("  }\n")
}

func (stringFlagType) emitShortNameParser(flagSetName, longName, shortName string) {
	panic("TODO")
}

func (stringFlagType) emitStartupParser(longName string) {
	panic("TODO")
}

type stringListFlagType struct{}

func (stringListFlagType) emitStructField(longName string) {
	fmt.Printf("%s []string\n", toSymbolName(longName, true))
}

func (stringListFlagType) emitDefaultInitializer(longName string) {
	fmt.Printf("f.%s = nil\n", toSymbolName(longName, true))
}

func (stringListFlagType) emitLongNameParser(flagSetName, longName string) {
	fmt.Printf("case %#v:\n", "--"+longName)
	fmt.Printf("  var out *[]string\n")
	fmt.Printf("  if flags := cmd.get%sFlags(); flags != nil {\n", toSymbolName(flagSetName, true))
	fmt.Printf("    out = &flags.%s\n", toSymbolName(longName, true))
	fmt.Printf("  } else if mustApply {\n")
	fmt.Printf("    return FlagNotApplicableError{Flag: longOptionName}\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if assignmentIndex < 0 {\n")
	fmt.Printf("    if len(*currentArgs) == 0 {\n")
	fmt.Printf("      return FlagMissingValueError{Flag: longOptionName}\n")
	fmt.Printf("    }\n")
	fmt.Printf("    optionValue = (*currentArgs)[0]\n")
	fmt.Printf("    (*currentArgs) = (*currentArgs)[1:]\n")
	fmt.Printf("  }\n")
	fmt.Printf("  if out != nil {\n")
	fmt.Printf("    *out = append(*out, optionValue)\n")
	fmt.Printf("  }\n")
}

func (stringListFlagType) emitShortNameParser(flagSetName, longName, shortName string) {
	panic("TODO")
}

func (stringListFlagType) emitStartupParser(longName string) {
	longSymbolName := toSymbolName(longName, true)

	fmt.Printf("case %#v:\n", "--"+longName)
	fmt.Printf("  if assignmentIndex < 0 {\n")
	fmt.Printf("    if argsIndex == len(args) {\n")
	fmt.Printf("      return nil, 0, FlagMissingValueError{Flag: longOptionName}\n")
	fmt.Printf("    }\n")
	fmt.Printf("    optionValue = args[argsIndex]\n")
	fmt.Printf("    argsIndex++\n")
	fmt.Printf("  }\n")
	fmt.Printf("  flags.%s = append(flags.%s, optionValue)\n", longSymbolName, longSymbolName)
}
