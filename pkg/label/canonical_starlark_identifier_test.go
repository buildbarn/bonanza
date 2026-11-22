package label_test

import (
	"testing"

	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/assert"
)

func TestCanonicalStarlarkIdentifier(t *testing.T) {
	t.Run("ToModuleExtension", func(t *testing.T) {
		for input, output := range map[string]string{
			"@@bazel_features+//private:extensions.bzl%version_extension":                "bazel_features++ui3r3a+version_extension",
			"@@bazel_features+1.2.3//private:extensions.bzl%version_extension":           "bazel_features+1.2.3+ui3r3a+version_extension",
			"@@bazel_features+//private%version_extension":                               "bazel_features++br7s5o+version_extension",
			"@@bazel_features++abcdef+foo+bar//private:extensions.bzl%version_extension": "bazel_features++fco4ai+version_extension",

			// Labels whose target name is redundant should
			// yield the same hash as their full form.
			"@@rules_foo+//foo.bzl:foo.bzl%ext": "rules_foo++vipnop+ext",
			"@@rules_foo+//foo.bzl%ext":         "rules_foo++vipnop+ext",

			// Module extensions with the same name declared in
			// different .bzl files should not collide.
			"@@rules_rust+//rust/private:internal_extensions.bzl%i":  "rules_rust++avr6ho+i",
			"@@rules_rust+//cargo/private:internal_extensions.bzl%i": "rules_rust++jb7eaw+i",
		} {
			moduleExtension := util.Must(label.NewCanonicalStarlarkIdentifier(input)).ToModuleExtension()
			assert.Equal(t, output, moduleExtension.String(), input)
			assert.False(t, moduleExtension.IsRepoRules(), input)
			assert.Equal(t, moduleExtension, util.Must(label.NewModuleExtension(output)), input)
		}
	})
}
