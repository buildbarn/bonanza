package label

import (
	"errors"
	"regexp"
	"strings"
)

// CanonicalPackage is a label string that refers to a package, meaning
// it does not include a target name. Furthermore, it is prefixed with a
// canonical repo name.
type CanonicalPackage struct {
	value string
}

const (
	validCanonicalPackagePattern                = `@@` + validCanonicalRepoPattern + `(//` + validNonEmptyPackageNamePattern + `)?`
	validApparentOrAbsoluteLabelPattern         = `(` + validApparentLabelPattern + `|` + validAbsoluteLabelPattern + `)`
	validRelativeTargetNameLabelPattern         = `((` + validNonEmptyPackageNamePattern + `)?:)?` + validTargetNamePattern
	validApparentOrAbsoluteTargetPatternPattern = `(` + validApparentTargetPatternPattern + `|` + validAbsoluteTargetPatternPattern + `)`
	validRelativeTargetNameTargetPatternPattern = `(` + validRelativeTargetNameLabelPattern + `|` + validRelativeRecursiveTargetPatternPattern + `)`
)

var (
	validCanonicalPackageRegexp                = regexp.MustCompile("^" + validCanonicalPackagePattern + "$")
	validApparentOrAbsoluteLabelRegexp         = regexp.MustCompile("^" + validApparentOrAbsoluteLabelPattern + "$")
	validRelativeTargetNameLabelRegexp         = regexp.MustCompile("^" + validRelativeTargetNameLabelPattern + "$")
	validApparentOrAbsoluteTargetPatternRegexp = regexp.MustCompile("^" + validApparentOrAbsoluteTargetPatternPattern + "$")
	validRelativeTargetNameTargetPatternRegexp = regexp.MustCompile("^" + validRelativeTargetNameTargetPatternPattern + "$")
)

var (
	errInvalidCanonicalPackage = errors.New("canonical package name must match " + validCanonicalPackagePattern)
	errInvalidLabel            = errors.New("label must match (" + validApparentOrAbsoluteLabelPattern + "|" + validRelativeTargetNameLabelPattern + ")")
	errInvalidTargetPattern    = errors.New("target pattern must match (" + validApparentOrAbsoluteTargetPatternPattern + "|" + validRelativeTargetNameTargetPatternPattern + ")")
)

// NewCanonicalPackage validates that the provided string value is a
// canonical package name. If so, an instance of CanonicalPackage is
// returned that wraps the value.
func NewCanonicalPackage(value string) (CanonicalPackage, error) {
	if !validCanonicalPackageRegexp.MatchString(value) {
		return CanonicalPackage{}, errInvalidCanonicalPackage
	}
	return CanonicalPackage{value: value}, nil
}

// GetCanonicalRepo returns the name of the canonical repo that contains
// this package.
func (p CanonicalPackage) GetCanonicalRepo() CanonicalRepo {
	repo := p.value[2:]
	if offset := strings.IndexByte(repo, '/'); offset >= 0 {
		repo = repo[:offset]
	}
	return CanonicalRepo{value: repo}
}

// GetPackagePath returns the path of the package, relative to the root
// of this repo. For the root package the empty string is returned.
func (p CanonicalPackage) GetPackagePath() string {
	if offset := strings.IndexByte(p.value, '/'); offset >= 0 {
		return p.value[offset+2:]
	}
	return ""
}

func (p CanonicalPackage) concatenateApparentOrAbsolute(value string) string {
	switch value[0] {
	case '@':
		// Provided label is already apparent.
		return value
	case '/':
		// Provided label is absolute.
		repo := p.value
		if offset := strings.IndexByte(repo, '/'); offset >= 0 {
			repo = repo[:offset]
		}
		return repo + value
	default:
		panic("pattern matched labels that were not apparent or absolute")
	}
}

func (p CanonicalPackage) concatenateRelative(value string, forceHasPackage bool) string {
	midfix := ""
	if forceHasPackage || strings.IndexByte(value, ':') >= 0 {
		// Provided label is relative.
		if strings.IndexByte(p.value, '/') < 0 {
			midfix = "//"
		} else if value[0] != ':' {
			midfix = "/"
		}
	} else {
		// Provided label is a bare target name. Bazel allows
		// those to refer to targets inside the longest matching
		// subpackage (e.g., "a/b/c" maps to either "a/b/c:c",
		// "a/b:c", "a:b/c", or ":a/b/c").
		//
		// We don't support this scheme, as it significantly
		// complicates the resolution process. We always
		// interpret such labels as refering to targets in the
		// current package (i.e., always "a/b/c" maps to
		// ":a/b/c").
		midfix = ":"
		if strings.IndexByte(p.value, '/') < 0 {
			midfix = "//:"
		}
	}
	return p.value + midfix + value
}

// AppendLabel appends a provided label value to a canonical package
// name. The resulting label is apparent, because the provided label
// value may be prefixed with an apparent repo. It is the caller's
// responsibility to resolve the apparent repo to a canonical one.
func (p CanonicalPackage) AppendLabel(value string) (ApparentLabel, error) {
	// Parse apparent/absolute labels and relative labels
	// separately. Values such as "@hello" are both valid apparent
	// labels and bare target names. We should prefer parsing those
	// as apparent labels. On the other hand, "@hello/foo" can only
	// be a bare target name, because apparent repo names need to be
	// followed by two slashes.
	if validApparentOrAbsoluteLabelRegexp.MatchString(value) {
		return newValidApparentLabel(p.concatenateApparentOrAbsolute(value)), nil
	}
	if validRelativeTargetNameLabelRegexp.MatchString(value) {
		return newValidApparentLabel(p.concatenateRelative(value, false)), nil
	}
	return ApparentLabel{}, errInvalidLabel
}

// AppendTargetName appends a target name to a canonical package name,
// thereby turning it into a canonical label.
func (p CanonicalPackage) AppendTargetName(targetName TargetName) CanonicalLabel {
	midfix := ":"
	if strings.IndexByte(p.value, '/') < 0 {
		midfix = "//:"
	}
	return newValidCanonicalLabel(p.value + midfix + targetName.value)
}

// AppendTargetPattern appends a provided target pattern value to a
// canonical package name. The resulting target pattern is apparent,
// because the provided label value may be prefixed with an apparent
// repo. It is the caller's responsibility to resolve the apparent repo
// to a canonical one.
func (p CanonicalPackage) AppendTargetPattern(value string) (ApparentTargetPattern, error) {
	if validApparentOrAbsoluteTargetPatternRegexp.MatchString(value) {
		return newValidApparentTargetPattern(p.concatenateApparentOrAbsolute(value)), nil
	}
	if validRelativeTargetNameTargetPatternRegexp.MatchString(value) {
		// Even though "a/b/c" should be interpreted as a target
		// name (":a/b/c"), "a/b/..." should be interpreted as a
		// wildcard package match.
		forceHasPackage := value == "..." || strings.HasSuffix(value, "/...")
		return newValidApparentTargetPattern(p.concatenateRelative(value, forceHasPackage)), nil
	}
	return ApparentTargetPattern{}, errInvalidTargetPattern
}

var validNonEmptyPackageNameRegexp = regexp.MustCompile("^" + validNonEmptyPackageNamePattern + "$")

var errInvalidNonEmptyPackageName = errors.New("non-empty package name must match " + validNonEmptyPackageNamePattern)

// isRootPackage returns whether the package is the root package of a
// repo (e.g., "@@a+"), as opposed to a package inside of it (e.g.,
// "@@a+//b").
func (p CanonicalPackage) isRootPackage() bool {
	return strings.IndexByte(p.value, '/') < 0
}

// IsAtOrBelow returns true if the package is identical to another
// package, or is located in a directory below it within the same repo.
func (p CanonicalPackage) IsAtOrBelow(other CanonicalPackage) bool {
	// For the root package of a repo (e.g., "@@a+"), the next
	// character is the first slash of "//".
	return strings.HasPrefix(p.value, other.value) &&
		(len(p.value) == len(other.value) || p.value[len(other.value)] == '/')
}

// AppendPackagePath appends a relative package path to a canonical
// package name, yielding the name of a package below it.
//
// This method can be used during expansion of recursive target patterns
// to split up work, so that subpackages can be traversed in parallel.
func (p CanonicalPackage) AppendPackagePath(pathBelow string) (CanonicalPackage, error) {
	if !validNonEmptyPackageNameRegexp.MatchString(pathBelow) {
		return CanonicalPackage{}, errInvalidNonEmptyPackageName
	}
	if p.isRootPackage() {
		return CanonicalPackage{value: p.value + "//" + pathBelow}, nil
	}
	return CanonicalPackage{value: p.value + "/" + pathBelow}, nil
}

// ToRecursiveTargetPattern returns a target pattern that matches all
// rule targets in the package and the packages below it ("/..."), or
// all targets including file targets ("/...:*").
//
// This method is the inverse of
// CanonicalTargetPattern.AsRecursiveTargetPattern().
func (p CanonicalPackage) ToRecursiveTargetPattern(includeFileTargets bool) CanonicalTargetPattern {
	targetPattern := p.value
	if p.isRootPackage() {
		targetPattern += "//..."
	} else {
		targetPattern += "/..."
	}
	if includeFileTargets {
		targetPattern += ":*"
	}
	return CanonicalTargetPattern{value: targetPattern}
}

// ToTargetsQuery returns a query that yields all rule targets in the
// package, or all targets including file targets. This is used to
// evaluate queries containing recursive target patterns for each
// package separately.
//
// The query is of shape "siblings(//foo:*)" or "rules(siblings(//foo:*))".
// Target patterns like "//foo:all" and "//foo:*" cannot be used
// directly, as they only match a single target if the package contains
// a target named "all" or "*". siblings() does not suffer from this
// problem, as any target matched by "//foo:*" yields all targets in
// the package.
func (p CanonicalPackage) ToTargetsQuery(includeFileTargets bool) Query[CanonicalTargetPattern] {
	targetPattern := p.value
	if p.isRootPackage() {
		targetPattern += "//"
	}
	expressions := []QueryExpression[CanonicalTargetPattern]{
		&QuerySet[CanonicalTargetPattern]{
			TargetPatterns: []CanonicalTargetPattern{{value: targetPattern + ":*"}},
		},
		&QuerySiblings[CanonicalTargetPattern]{Expression: 0},
	}
	if !includeFileTargets {
		expressions = append(expressions, &QueryRules[CanonicalTargetPattern]{Expression: 1})
	}
	return Query[CanonicalTargetPattern]{expressions: expressions}
}

// AsCanonicalLabel returns the label of the package, being the label
// whose target name is identical to the last component of the package
// path (e.g., "@@a+//b" for "@@a+//b:b"), or the canonical repo name
// for the root package of a repo (e.g., "@@a+" for "@@a+//:a+").
func (p CanonicalPackage) AsCanonicalLabel() CanonicalLabel {
	return CanonicalLabel{value: p.value}
}

func (p CanonicalPackage) String() string {
	return p.value
}
