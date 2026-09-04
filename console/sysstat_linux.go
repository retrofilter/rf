package console

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func readMemStat() (SysStat, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return SysStat{}, err
	}
	return parseMemStat(string(b))
}

func parseMemStat(meminfo string) (SysStat, error) {
	var total, avail uint64
	for _, line := range strings.Split(meminfo, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "MemTotal", "MemAvailable":
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				return SysStat{}, errors.New("unrecognized /proc/meminfo")
			}
			v, err := strconv.ParseUint(fields[0], 10, 64)
			if err != nil {
				return SysStat{}, err
			}
			if key == "MemTotal" {
				total = v
			} else {
				avail = v
			}
		}
	}
	if total == 0 || avail > total {
		return SysStat{}, errors.New("unrecognized /proc/meminfo")
	}
	used := total - avail
	return SysStat{Mem: 100 * float64(used) / float64(total), UsedKB: used}, nil
}
