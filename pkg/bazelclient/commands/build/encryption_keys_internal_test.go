package build

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeriveEncryptionKeys(t *testing.T) {
	seed := []byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	}

	t.Run("SeedTooShort", func(t *testing.T) {
		_, err := deriveEncryptionKeys(seed[:15])
		require.EqualError(t, err, "seed is 15 bytes in size, while at least 16 bytes are required")
	})

	t.Run("KnownAnswer", func(t *testing.T) {
		// Changing how keys are derived causes all previously
		// stored objects to become unusable. These values
		// ensure this doesn't happen by accident.
		keys, err := deriveEncryptionKeys(seed)
		require.NoError(t, err)
		require.Equal(t, "ad09cf26687b0e9f204c4bc158ec8fc3cd8b77f9dc5a617860a540e756b09357", hex.EncodeToString(keys.directories))
		require.Equal(t, "280d34311817fb26ec4bc24bd34a187197c90827120cb364704ff4cfe239817c", hex.EncodeToString(keys.analysis))
	})

	t.Run("Distinct", func(t *testing.T) {
		keys, err := deriveEncryptionKeys(seed)
		require.NoError(t, err)
		seen := map[string]struct{}{}
		for _, key := range [][]byte{
			keys.directories,
			keys.fileChunks,
			keys.fileContentsLists,
			keys.chunkGearTableSeed,
			keys.actions,
			keys.evaluation,
			keys.analysis,
			keys.starlarkValues,
		} {
			require.Len(t, key, derivedKeySizeBytes)
			seen[string(key)] = struct{}{}
		}
		require.Len(t, seen, 8)
	})
}
