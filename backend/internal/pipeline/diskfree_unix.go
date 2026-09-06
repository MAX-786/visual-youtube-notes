//go:build !windows

package pipeline

import "golang.org/x/sys/unix"

// freeBytes is the space available to this process on the volume holding path.
func freeBytes(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // block counts fit comfortably
}
