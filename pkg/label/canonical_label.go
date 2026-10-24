package label

import (
	"errors"
	"regexp"
	"strings"
)

// CanonicalLabel is a label string that is prefixed with a canonical
// repo name. This type can be used to refer to a single target in an
// unambiguous way.
type CanonicalLabel struct {
	value string
}

const (
	// Components of package and target names may contain all
	// printable characters, except backslash, forward slash, and
	// colon.
	validPackageTargetNameCharacterPattern       = `[^[:cntrl:]/:\\]`
	validPackageTargetNameNonDotCharacterPattern = `[^[:cntrl:]./:\\]`

	// Package name components may not consist solely of dots.
	validPackageNameComponentPattern = validPackageTargetNameCharacterPattern + `*` +
		validPackageTargetNameNonDotCharacterPattern +
		validPackageTargetNameCharacterPattern + `*`
	validNonEmptyPackageNamePattern = validPackageNameComponentPattern + `(/` + validPackageNameComponentPattern + `)*`

	// Target name components may consist solely of dots, as long as
	// there are more than two. Valid target name components MUST be
	// a superset of valid package name components. Otherwise, it
	// wouldn't be safe to translate //a/b to //a/b:b.
	validTargetNameComponentPattern = `(` + validPackageNameComponentPattern + `|\.\.+)`
	validTargetNamePattern          = validTargetNameComponentPattern + `(/` + validTargetNameComponentPattern + `)*`

	validAbsoluteLabelPattern = `//(` +
		`:` + validTargetNamePattern + `|` +
		validNonEmptyPackageNamePattern + `(:` + validTargetNamePattern + `)?` +
		`)`
	validMaybeAbsoluteLabelPattern = `(` + validAbsoluteLabelPattern + `)?`

	validCanonicalLabelPattern = `@@` + validCanonicalRepoPattern + validMaybeAbsoluteLabelPattern
)

var validCanonicalLabelRegexp = regexp.MustCompile("^" + validCanonicalLabelPattern + "$")

var errInvalidCanonicalLabelPattern = errors.New("canonical label must match " + validCanonicalLabelPattern)

// removeLabelTargetNameIfRedundant checks whether the target name
// contained in a label is identical to the last component of the repo
// name (@foo//:foo) or package path (@foo//bar:bar). If so, the target
// name is removed.
func removeLabelTargetNameIfRedundant(label string) string {
	if targetNameOffset := strings.IndexByte(label, ':'); targetNameOffset >= 0 {
		targetName := label[targetNameOffset+1:]
		canonicalPackage := label[:targetNameOffset]
		var lastComponent string
		if canonicalRepo, ok := strings.CutSuffix(canonicalPackage, "//"); ok {
			if strings.HasPrefix(canonicalRepo, "@@[") {
				// Resolved label of shape
				// "@@[error message]//:foo". These
				// labels should always keep their
				// target name. We shouldn't convert
				// "@@[x]//:[x]" to "@@[x]".
				return label
			}
			canonicalPackage = canonicalRepo
			lastComponent = strings.TrimLeft(canonicalPackage, "@")
		} else {
			lastComponent = canonicalPackage[strings.LastIndexByte(canonicalPackage, '/')+1:]
		}
		if targetName == lastComponent {
			label = canonicalPackage
		}
	}
	return label
}

// NewCanonicalLabel creates a new CanonicalLabel based on the provided
// label value.
func NewCanonicalLabel(value string) (CanonicalLabel, error) {
	if !validCanonicalLabelRegexp.MatchString(value) {
		return CanonicalLabel{}, errInvalidCanonicalLabelPattern
	}
	return newValidCanonicalLabel(value), nil
}

func newValidCanonicalLabel(value string) CanonicalLabel {
	return CanonicalLabel{value: removeLabelTargetNameIfRedundant(value)}
}

// GetCanonicalPackage strips the target name from a label, thereby
// returning the canonical package name.
func (l CanonicalLabel) GetCanonicalPackage() CanonicalPackage {
	if offset := strings.IndexByte(l.value, ':'); offset >= 0 {
		return CanonicalPackage{value: strings.TrimSuffix(l.value[:offset], "//")}
	}
	return CanonicalPackage{value: l.value}
}

// GetCanonicalRepo returns the name of the canonical repo that is
// embedded in the label.
func (l CanonicalLabel) GetCanonicalRepo() CanonicalRepo {
	repo := l.value[2:]
	if offset := strings.IndexByte(repo, '/'); offset >= 0 {
		repo = repo[:offset]
	}
	return CanonicalRepo{value: repo}
}

// GetTargetName returns the name of the target within a package.
func (l CanonicalLabel) GetTargetName() TargetName {
	if offset := strings.IndexByte(l.value, ':'); offset >= 0 {
		// Label has an explicit target name.
		return TargetName{value: l.value[offset+1:]}
	}
	if offset := strings.LastIndexByte(l.value, '/'); offset >= 0 {
		// Derive the target name from the last component.
		return TargetName{value: l.value[offset+1:]}
	}
	// Derive the target name from the repo name.
	return TargetName{value: strings.TrimLeft(l.value, "@")}
}

// GetExternalRelativePath converts a label to a pathname string that is
// relative to the "external/" directory containing all repos. This can
// be used in contexts where labels refer to paths of input files.
func (l CanonicalLabel) GetExternalRelativePath() string {
	label := l.value[2:]
	if targetNameOffset := strings.IndexByte(label, ':'); targetNameOffset >= 0 {
		repoAndPackage := label[:targetNameOffset]
		targetName := label[targetNameOffset+1:]
		slashOffset := strings.IndexByte(repoAndPackage, '/')
		repoSlash := repoAndPackage[:slashOffset+1]
		pkg := repoAndPackage[slashOffset+2:]
		if len(pkg) == 0 {
			// @@a//:b -> a/b.
			return repoSlash + targetName
		}
		// @@a//b/c:d -> a/b/c/d.
		return repoSlash + pkg + "/" + targetName
	}
	if slashOffset := strings.IndexByte(label, '/'); slashOffset >= 0 {
		// @@a//b/c -> a/b/c/c.
		return label[:slashOffset+1] + label[slashOffset+2:] + "/" + label[strings.LastIndexByte(label, '/')+1:]
	}
	// @@a -> a/a.
	return label + "/" + label
}

// GetRepoRelativePath converts a label to a pathname string that is
// relative to the root of the repo. This can be used in contexts where
// labels refer to paths of input files.
func (l CanonicalLabel) GetRepoRelativePath() string {
	label := l.value[2:]
	if targetNameOffset := strings.IndexByte(label, ':'); targetNameOffset >= 0 {
		repoAndPackage := label[:targetNameOffset]
		targetName := label[targetNameOffset+1:]
		pkg := repoAndPackage[strings.IndexByte(repoAndPackage, '/')+2:]
		if len(pkg) == 0 {
			// @@a//:b -> b.
			return targetName
		}
		// @@a//b/c:d -> b/c/d.
		return pkg + "/" + targetName
	}
	if slashOffset := strings.IndexByte(label, '/'); slashOffset >= 0 {
		// @@a//b/c -> b/c/c.
		return label[slashOffset+2:] + "/" + label[strings.LastIndexByte(label, '/')+1:]
	}
	// @@a -> a.
	return label
}

func (l CanonicalLabel) String() string {
	return l.value
}

// Compare two canonical labels, returning -1, 0 or 1 if the first label
// is respectively smaller than, equal to or greater than the second.
//
// Labels are ordered by canonical repo name, followed by the components
// of the package path, followed by the target name. This corresponds to
// the order in which targets are encountered when traversing the
// directory hierarchy of a repo depth first, where the targets in a
// package precede the targets in its subpackages.
func (l CanonicalLabel) Compare(other CanonicalLabel) int {
	if c := strings.Compare(l.GetCanonicalRepo().value, other.GetCanonicalRepo().value); c != 0 {
		return c
	}
	if c := comparePackagePaths(l.GetCanonicalPackage().GetPackagePath(), other.GetCanonicalPackage().GetPackagePath()); c != 0 {
		return c
	}
	return strings.Compare(l.GetTargetName().value, other.GetTargetName().value)
}

// comparePackagePaths compares two package paths by their components.
func comparePackagePaths(a, b string) int {
	for a != b {
		if a == "" {
			return -1
		}
		if b == "" {
			return 1
		}
		aFirst, aRest, _ := strings.Cut(a, "/")
		bFirst, bRest, _ := strings.Cut(b, "/")
		if c := strings.Compare(aFirst, bFirst); c != 0 {
			return c
		}
		a, b = aRest, bRest
	}
	return 0
}

// AppendStarlarkIdentifier appends a Starlark identifier to a label,
// resulting in the canonical name of a Starlark identifier.
func (l CanonicalLabel) AppendStarlarkIdentifier(identifier StarlarkIdentifier) CanonicalStarlarkIdentifier {
	return CanonicalStarlarkIdentifier{
		value: l.value + "%" + identifier.value,
	}
}

// AsResolved converts a canonical label to a resolved label. As
// canonical labels can always be resolved successfully, this function
// leaves the value as is.
func (l CanonicalLabel) AsResolved() ResolvedLabel {
	return ResolvedLabel{value: l.value}
}
