//go:build windows

package diskfree

import "golang.org/x/sys/windows"

// GetDiskFreeSpaceEx's first out-parameter is the quota-aware figure —
// what *this user* may still write — which is the direct analogue of
// statfs' Bavail and the right number for a precheck.
func available(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		return 0, err
	}
	return freeToCaller, nil
}
