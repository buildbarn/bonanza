package label

import (
	"errors"
	"regexp"
	"strings"
)

// CanonicalTargetPattern is a target pattern string that is prefixed
// with a canonical repo name. This type can be used to refer to zero or
// more targets in an unambiguous way.
type CanonicalTargetPattern struct {
	value string
}

const (
	validRelativeRecursiveTargetPatternPattern = `(` + validPackageNameComponentPattern + `/)*\.\.\.(:(all|all-targets|\*))?`
	validAbsoluteTargetPatternPattern          = `//(` +
		`:` + validTargetNamePattern + `|` +
		validNonEmptyPackageNamePattern + `(:` + validTargetNamePattern + `)?|` +
		validRelativeRecursiveTargetPatternPattern +
		`)`
	validMaybeAbsoluteTargetPatternPattern = `(` + validAbsoluteTargetPatternPattern + `)?`

	validCanonicalTargetPatternPattern = `@@` + validCanonicalRepoPattern + validMaybeAbsoluteTargetPatternPattern
)

var validCanonicalTargetPatternRegexp = regexp.MustCompile("^" + validCanonicalTargetPatternPattern + "$")

var errInvalidCanonicalTargetPattern = errors.New("canonical target pattern must match " + validCanonicalTargetPatternPattern)

// removeTargetPatternTargetNameIfRedundant checks whether the target
// name contained in a target pattern is identical to the last component
// of the repo name (@foo//:foo) or package path (@foo//bar:bar). If so,
// the target name is removed.
//
// This function differs from removeLabelTargetNameIfRedundant in that
// it does not remove the target name if it is equal to "all",
// "all-targets" or "*". Those need to be kept to ensure wildcard
// matching is performed. The only exception is if "//foo/...:all" is
// used, because in that case ":all" is implied.
func removeTargetPatternTargetNameIfRedundant(targetPattern string) string {
	if targetNameOffset := strings.IndexByte(targetPattern, ':'); targetNameOffset >= 0 {
		targetName := targetPattern[targetNameOffset+1:]
		canonicalPackage := targetPattern[:targetNameOffset]
		var lastComponent string
		if canonicalRepo, ok := strings.CutSuffix(canonicalPackage, "//"); ok {
			canonicalPackage = canonicalRepo
			lastComponent = strings.TrimLeft(canonicalPackage, "@")
		} else {
			lastComponent = canonicalPackage[strings.LastIndexByte(canonicalPackage, '/')+1:]
		}
		switch targetName {
		case "all":
			if lastComponent == "..." {
				targetPattern = canonicalPackage
			}
		case "all-targets":
			// Rewrite "//foo/...:all-targets" to
			// "//foo/...:*" for brevity.
			if lastComponent == "..." {
				targetPattern = canonicalPackage + ":*"
			}
		case "*":
		default:
			if targetName == lastComponent {
				targetPattern = canonicalPackage
			}
		}
	}
	return targetPattern
}

// NewCanonicalTargetPattern creates a new CanonicalTargetPattern based
// on the provided target pattern value.
func NewCanonicalTargetPattern(value string) (CanonicalTargetPattern, error) {
	if !validCanonicalTargetPatternRegexp.MatchString(value) {
		return CanonicalTargetPattern{}, errInvalidCanonicalTargetPattern
	}
	return newValidCanonicalTargetPattern(value), nil
}

func newValidCanonicalTargetPattern(value string) CanonicalTargetPattern {
	return CanonicalTargetPattern{value: removeTargetPatternTargetNameIfRedundant(value)}
}

func (tp CanonicalTargetPattern) String() string {
	return tp.value
}

// targetPatternHasWildcard returns whether a target pattern contains a
// wildcard, meaning that it may refer to more than one target.
func targetPatternHasWildcard(targetPattern string) bool {
	return strings.HasSuffix(targetPattern, "/...") || strings.HasSuffix(targetPattern, ":all") || strings.HasSuffix(targetPattern, ":all-targets") || strings.HasSuffix(targetPattern, ":*")
}

// AsCanonicalLabel converts a canonical target pattern to a canonical
// label. Conversion only succeeds if the pattern is guaranteed to refer
// to a single target.
func (tp CanonicalTargetPattern) AsCanonicalLabel() (CanonicalLabel, bool) {
	if targetPatternHasWildcard(tp.value) {
		return CanonicalLabel{}, false
	}
	return CanonicalLabel{value: tp.value}, true
}

// AsSinglePackageTargetPattern returns success if the target pattern
// refers to a single package and contains a wildcard target name
// (":all", ":all-targets", or ":*").
//
// Upon success, a label is returned that corresponds to the target
// pattern. The caller is expected to first check whether a target
// exists for that name. Only if it does not exist, wildcard expansion
// should be performed.
func (tp CanonicalTargetPattern) AsSinglePackageTargetPattern() (initialTarget CanonicalLabel, includeFileTargets, success bool) {
	targetNameOffset := strings.IndexByte(tp.value, ':')
	if targetNameOffset < 0 {
		// Target patterns of shape "@@a+//b/c" always refer to
		// a single target.
		return initialTarget, includeFileTargets, success
	}
	if strings.HasSuffix(tp.value[:targetNameOffset], "/...") {
		// Recursive target pattern.
		return initialTarget, includeFileTargets, success
	}
	switch tp.value[targetNameOffset+1:] {
	case "all":
	case "all-targets":
		includeFileTargets = true
	case "*":
		includeFileTargets = true
	default:
		// Target pattern that includes a target name that does
		// not require expansion.
		return initialTarget, includeFileTargets, success
	}

	return newValidCanonicalLabel(tp.value), includeFileTargets, true
}

// AsRecursiveTargetPattern returns success if the target pattern
// matches packages recursively (i.e., it ends with "/..." or "/..:*").
//
// Upon success, the package name is returned at which recursive
// expansion should start.
func (tp CanonicalTargetPattern) AsRecursiveTargetPattern() (basePackage CanonicalPackage, includeFileTargets, success bool) {
	packageName, ok := strings.CutSuffix(tp.value, "/...")
	if !ok {
		packageName, ok = strings.CutSuffix(tp.value, "/...:*")
		if !ok {
			return basePackage, includeFileTargets, success
		}
		includeFileTargets = true
	}
	return CanonicalPackage{value: strings.TrimSuffix(packageName, "/")}, includeFileTargets, true
}

// ToQuery returns a query that yields the targets matched by the target
// pattern.
func (tp CanonicalTargetPattern) ToQuery() Query[CanonicalTargetPattern] {
	return Query[CanonicalTargetPattern]{
		expressions: []QueryExpression[CanonicalTargetPattern]{
			&QuerySet[CanonicalTargetPattern]{
				TargetPatterns: []CanonicalTargetPattern{tp},
			},
		},
	}
}

// ToQueryExcludingManualTargets returns a query that yields the targets
// matched by the target pattern, excluding targets having tag "manual"
// if the target pattern contains wildcards. This matches the behavior
// of target patterns provided to commands like "bazel build". Target
// patterns that refer to a single target are left unmodified, as such
// targets should always be included.
//
// Target patterns containing wildcards are rewritten to queries of
// shape "p - tag(manual, p)".
func (tp CanonicalTargetPattern) ToQueryExcludingManualTargets() Query[CanonicalTargetPattern] {
	if _, ok := tp.AsCanonicalLabel(); ok {
		return tp.ToQuery()
	}
	return Query[CanonicalTargetPattern]{
		expressions: []QueryExpression[CanonicalTargetPattern]{
			&QuerySet[CanonicalTargetPattern]{
				TargetPatterns: []CanonicalTargetPattern{tp},
			},
			&QueryTag[CanonicalTargetPattern]{
				Tag:        "manual",
				Expression: 0,
			},
			&QuerySetOperation[CanonicalTargetPattern]{
				Operator: QuerySetOperatorExcept,
				Left:     0,
				Right:    1,
			},
		},
	}
}
