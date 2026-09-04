package console

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const vmstatSample = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                             1686632.
Pages active:                           1057341.
Pages inactive:                         1114302.
Pages speculative:                        94381.
Pages throttled:                              0.
Pages wired down:                        164904.
Pages purgeable:                          40174.
"Translation faults":                7121919769.
Pages copy-on-write:                  653167956.
Pages zero filled:                   3360683557.
Pages reactivated:                    166473796.
Pages purged:                          39280364.
File-backed pages:                      1159493.
Anonymous pages:                        1106531.
Pages stored in compressor:              280904.
Pages occupied by compressor:             21838.
Decompressions:                        81391787.
Compressions:                          90927789.
Pageins:                              174947732.
Pageouts:                                131743.
Swapins:                                3377491.
Swapouts:                               4671447.
`

func TestParseMemStatDarwin(t *testing.T) {
	// used = wired + compressor-occupied + (anonymous − purgeable), in pages
	// of 16384 bytes, against a 64 GiB machine.
	const totalBytes = 64 << 30
	used := uint64(164904+21838+1106531-40174) * 16384
	stat, err := parseMemStat(vmstatSample, totalBytes)
	require.NoError(t, err)
	require.InDelta(t, 100*float64(used)/float64(totalBytes), stat.Mem, 0.001)
	require.Equal(t, used/1024, stat.UsedKB)

	// Missing counters, missing page size, or a zero total are unrecognized.
	_, err = parseMemStat("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10.\n", totalBytes)
	require.Error(t, err)
	_, err = parseMemStat("Pages wired down: 10.\nAnonymous pages: 10.\n", totalBytes)
	require.Error(t, err)
	_, err = parseMemStat(vmstatSample, 0)
	require.Error(t, err)

	// Used can never exceed 100%.
	stat, err = parseMemStat(vmstatSample, 1)
	require.NoError(t, err)
	require.InDelta(t, 100.0, stat.Mem, 0.001)
}
