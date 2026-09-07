//go:build windows

package health

import "golang.org/x/sys/windows"

// usage is the space available to this process, and the size, of the
// volume holding path.
func usage(path string) (diskUsage, error) {
	var free, total, totalFree uint64
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return diskUsage{}, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return diskUsage{}, err
	}
	return diskUsage{free: int64(free), total: int64(total)}, nil //nolint:gosec // a disk's bytes fit in int64
}
