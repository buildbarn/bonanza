package label_test

import (
	"testing"

	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/assert"
)

func TestModuleExtension(t *testing.T) {
	t.Run("GetModuleInstance", func(t *testing.T) {
		assert.Equal(t, "rules_foo+", util.Must(label.NewModuleExtension("rules_foo++abcdef+ext")).GetModuleInstance().String())
		assert.Equal(t, "rules_foo+1.2.3", util.Must(label.NewModuleExtension("rules_foo+1.2.3+abcdef+ext")).GetModuleInstance().String())
		assert.Equal(t, "rules_foo+", util.Must(label.NewModuleExtension("rules_foo++")).GetModuleInstance().String())
		assert.Equal(t, "rules_foo+1.2.3", util.Must(label.NewModuleExtension("rules_foo+1.2.3+")).GetModuleInstance().String())
	})

	t.Run("IsRepoRules", func(t *testing.T) {
		assert.False(t, util.Must(label.NewModuleExtension("rules_foo++abcdef+ext")).IsRepoRules())
		assert.False(t, util.Must(label.NewModuleExtension("rules_foo+1.2.3+abcdef+ext")).IsRepoRules())
		assert.True(t, util.Must(label.NewModuleExtension("rules_foo++")).IsRepoRules())
		assert.True(t, util.Must(label.NewModuleExtension("rules_foo+1.2.3+")).IsRepoRules())
	})

	t.Run("Invalid", func(t *testing.T) {
		for _, input := range []string{
			"rules_foo+",
			"rules_foo+1.2.3",
			"rules_foo+++",
			"rules_foo++ext",
			"rules_foo++abcdef+",
			"rules_foo++ABCDEF+ext",
			"rules_foo++abcde+ext",
			"rules_foo++abcdef1+ext",
		} {
			_, err := label.NewModuleExtension(input)
			assert.Error(t, err, input)
		}
	})

	t.Run("GetRepoRulesModuleExtension", func(t *testing.T) {
		moduleExtension := util.Must(label.NewModuleInstance("rules_foo+")).GetRepoRulesModuleExtension()
		assert.Equal(t, "rules_foo++", moduleExtension.String())
		assert.True(t, moduleExtension.IsRepoRules())
		assert.Equal(t, "rules_foo+++my_repo", moduleExtension.GetCanonicalRepoWithModuleExtension(util.Must(label.NewApparentRepo("my_repo"))).String())
	})
}
