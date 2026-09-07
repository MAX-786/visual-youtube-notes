//go:build !windows

package health

import "golang.org/x/sys/unix"

// usage is the space available to this process, and the size, of the
// volume holding path.
func usage(path string) (diskUsage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return diskUsage{}, err
	}
	return diskUsage{
		free:  int64(st.Bavail) * int64(st.Bsize), //nolint:gosec // block counts fit comfortably
		total: int64(st.Blocks) * int64(st.Bsize), //nolint:gosec // block counts fit comfortably
	}, nil
}
