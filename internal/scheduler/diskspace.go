package scheduler

import "syscall"

// freeDiskBytesFunc is checkDiskSpace's only way of reading free space —
// a package-level var, not a direct freeDiskBytes call, so tests can
// swap it to force the low-disk-space branch without needing a real
// filesystem that's actually low on space.
var freeDiskBytesFunc = freeDiskBytes

// freeDiskBytes returns the space available to an unprivileged process
// on the filesystem containing path (Bavail, not Btotal-Bfree — the
// same distinction df uses, so this matches what a mount reserving space
// for root actually leaves usable). Statfs_t's field widths differ
// between Linux and Darwin (this only ever runs on Linux in production —
// see the project's platform policy — but is built and tested on Darwin
// during development too), hence the explicit uint64 conversions rather
// than relying on both fields already sharing one type.
func freeDiskBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
