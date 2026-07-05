package duplicate

import "syscall"

// mkfifo creates a named pipe for the non-regular-file skip test.
func mkfifo(path string) error { return syscall.Mkfifo(path, 0o644) }
