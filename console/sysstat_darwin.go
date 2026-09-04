package console

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// vm_stat reader behind sysStats — macOS has no /proc, and the Mach
// host_statistics64 call behind Activity Monitor needs cgo, so the page
// counters come from vm_stat (shipped with every macOS) and the total from
// the hw.memsize sysctl. Parsing is factored from reading so tests can feed
// literal vm_stat output (sysstat_darwin_test.go).

// readMemStat returns used memory, where used is Activity Monitor's
// definition — wired + compressor-occupied + (anonymous − purgeable) — so
// reclaimable file cache doesn't count, mirroring the MemAvailable-based
// number on Linux.
func readMemStat() (SysStat, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return SysStat{}, err
	}
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return SysStat{}, err
	}
	return parseMemStat(string(out), total)
}

func parseMemStat(vmstat string, totalBytes uint64) (SysStat, error) {
	var pageSize uint64
	counters := map[string]uint64{}
	for _, line := range strings.Split(vmstat, "\n") {
		// Header: "Mach Virtual Memory Statistics: (page size of 16384 bytes)"
		if _, rest, ok := strings.Cut(line, "(page size of "); ok {
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
				pageSize = v
			}
			continue
		}
		// Counters: "Pages wired down:                        164904."
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), "."), 10, 64)
		if err != nil {
			continue
		}
		counters[key] = v
	}
	wired, okWired := counters["Pages wired down"]
	anon, okAnon := counters["Anonymous pages"]
	if pageSize == 0 || totalBytes == 0 || !okWired || !okAnon {
		return SysStat{}, errors.New("unrecognized vm_stat output")
	}
	purgeable := min(counters["Pages purgeable"], anon)
	used := min((wired+counters["Pages occupied by compressor"]+anon-purgeable)*pageSize, totalBytes)
	return SysStat{Mem: 100 * float64(used) / float64(totalBytes), UsedKB: used / 1024}, nil
}
