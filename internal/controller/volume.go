package controller

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const volumeState = "private"
const volumeStaging = ".private-migration"

// Keep SQLite recovery files together. The database is moved last; publication
// is a single directory rename only after every file and directory is synced.
var volumeFiles = []string{"state.db.lock", "state.db-journal", "state.db-wal", "state.db-shm", "state.db"}

var errVolume = errors.New("state volume preparation failed: stop all volume users, preserve the volume, and follow deploy/README.md recovery instructions")

// PrepareVolume is an offline init-container operation, never part of Open.
// The PVC must be local, dedicated to this controller identity and quiescent.
// Recreate orders the managed pods; neither RWO nor these locks exclude rogue
// same-identity writers. No database pages are read or SQLite recovery attempted.
func PrepareVolume(path string) error { return prepareVolume(path, nil) }

func prepareVolume(path string, afterMove func() error) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errVolume
	}
	root := os.NewFile(uintptr(fd), path)
	defer root.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || st.Gid != uint32(os.Getegid()) || st.Mode&0002 != 0 {
		return errVolume
	}
	guard, err := volumeFile(root, ".prepare.lock", true)
	if err != nil {
		return err
	}
	defer guard.Close()
	if unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return errVolume
	}
	legacy, err := volumeEntries(root, true)
	if err != nil {
		return err
	}
	state, err := volumeDirectory(root, volumeState, false)
	if err != nil {
		return err
	}
	if state != nil {
		defer state.Close()
	}
	stage, err := volumeDirectory(root, volumeStaging, false)
	if err != nil {
		return err
	}
	if stage != nil {
		defer stage.Close()
	}
	// Interrupted migrations require operator recovery. Never guess whether
	// split root/staging artifacts belong to the same database generation.
	if stage != nil {
		return errVolume
	}
	var entries map[string]bool
	if state != nil {
		entries, err = volumeEntries(state, false)
		if err != nil || (len(entries) != 0 && len(legacy) != 0) {
			return errVolume
		}
	}
	if len(legacy) == 0 {
		if state == nil {
			state, err = volumeDirectory(root, volumeState, true)
			if err != nil {
				return err
			}
			defer state.Close()
		}
		if err := normalizeVolumeState(state, entries); err != nil {
			return err
		}
		if root.Sync() != nil {
			return errVolume
		}
		return nil
	}
	if !legacy["state.db"] {
		return errVolume // Orphan sidecars/locks are never silently discarded.
	}
	lock, err := volumeFile(root, "state.db.lock", true)
	if err != nil {
		return err
	}
	defer lock.Close()
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return errVolume
	}
	legacy["state.db.lock"] = true
	stage, err = volumeDirectory(root, volumeStaging, true)
	if err != nil {
		return err
	}
	defer stage.Close()
	if root.Sync() != nil {
		return errVolume
	}
	for _, name := range volumeFiles {
		if !legacy[name] {
			continue
		}
		file, err := volumeFile(root, name, false)
		if err != nil {
			return err
		}
		err = file.Sync()
		file.Close()
		if err != nil {
			return errVolume
		}
		if unix.Renameat(int(root.Fd()), name, int(stage.Fd()), name) != nil || stage.Sync() != nil || root.Sync() != nil {
			return errVolume
		}
		if afterMove != nil {
			if err := afterMove(); err != nil {
				return err
			}
		}
	}
	if stage.Sync() != nil || unix.Renameat(int(root.Fd()), volumeStaging, int(root.Fd()), volumeState) != nil || root.Sync() != nil {
		return errVolume
	}
	return nil
}

// Open relative to anchored directories. Reject foreign owners, extra links,
// symlinks and special files before changing any file's permissions.
func volumeFile(dir *os.File, name string, create bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags, 0600)
	if err != nil {
		return nil, errVolume
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !volumeRegular(st) || f.Chmod(0600) != nil {
		f.Close()
		return nil, errVolume
	}
	return f, nil
}

func volumeRegular(st unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1
}

func volumeDirectory(root *os.File, name string, create bool) (*os.File, error) {
	if create && unix.Mkdirat(int(root.Fd()), name, 0700) != nil {
		return nil, errVolume
	}
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT && !create {
		return nil, nil
	}
	if err != nil {
		return nil, errVolume
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || f.Chmod(0700) != nil {
		f.Close()
		return nil, errVolume
	}
	return f, nil
}

func volumeEntries(dir *os.File, root bool) (map[string]bool, error) {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, errVolume
	}
	entries := map[string]bool{}
	for _, name := range names {
		if root && !strings.HasPrefix(name, "state.db") {
			continue // Volume metadata (e.g. lost+found) is not controller state.
		}
		known := false
		for _, allowed := range volumeFiles {
			known = known || name == allowed
		}
		var st unix.Stat_t
		if !known || unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || !volumeRegular(st) {
			return nil, errVolume
		}
		entries[name] = true
	}
	return entries, nil
}

func normalizeVolumeState(dir *os.File, entries map[string]bool) error {
	if len(entries) == 0 {
		if dir.Sync() != nil {
			return errVolume
		}
		return nil
	}
	if !entries["state.db"] {
		return errVolume
	}
	lock, err := volumeFile(dir, "state.db.lock", true)
	if err != nil {
		return err
	}
	defer lock.Close()
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return errVolume
	}
	for name := range entries {
		file, err := volumeFile(dir, name, false)
		if err != nil {
			return err
		}
		err = file.Sync()
		file.Close()
		if err != nil {
			return errVolume
		}
	}
	if dir.Sync() != nil {
		return errVolume
	}
	return nil
}
