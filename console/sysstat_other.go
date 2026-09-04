//go:build !linux && !darwin

package console

import "errors"

// No memory source off Linux/macOS: the constructor never primes, Get returns
// nil, and the Mem bar simply stays hidden.

func readMemStat() (SysStat, error) {
	return SysStat{}, errors.New("host stats unsupported on this platform")
}
