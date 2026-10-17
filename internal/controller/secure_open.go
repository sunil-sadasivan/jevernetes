package controller

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
	"modernc.org/libc"
	"modernc.org/libc/sys/types"
	sqlite3 "modernc.org/sqlite/lib"
)

// This adapter is intentionally tied to the pinned modernc Unix VFS ABI. The
// default VFS retains its locking, journal recovery and synchronization methods.
// Only xOpen is wrapped: it must use no-follow, and its actual descriptor must
// match the descriptor we verified before SQLite may read any database pages.
// SHM bypasses xOpen; Open validates that leaf before any SQLite initialization.
type verifiedVFS struct {
	ptr       uintptr
	name      string
	expected  unix.Stat_t
	baseOpen  uintptr
	directory *os.File
}

var vfsSerial atomic.Uint64
var verifiedFiles sync.Map // VFS pointer -> *verifiedVFS

func privateStat(fd int) (unix.Stat_t, error) { return privateDescriptorStat(fd, false) }
func privateDescriptorStat(fd int, temporary bool) (unix.Stat_t, error) {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) || (st.Nlink != 1 && !(temporary && st.Nlink == 0)) {
		return st, ErrState
	}
	return st, nil
}
func openPrivate(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrState
	}
	f := os.NewFile(uintptr(fd), path)
	if _, err := privateStat(fd); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// The pinned Unix VFS opens SHM directly in unixOpenSharedMemory (xShmMap),
// including a blocking read-only fallback, and truncates it in
// unixLockSharedMemory. Validate before SQLite can reach either operation.
// Do not create an absent SHM: DELETE databases do not need it, and SQLite
// creates it using the verified database's private mode if WAL recovery needs it.
// The anchored 0700 directory excludes other identities. Root/same-identity
// replacement between this check and SQLite's path-based open remains possible;
// proactive creation would not close that race. See docs/controller.md.
func validateSharedMemory(directory *os.File, name string) error {
	// Reject special files before opening even nonblocking (e.g. devices).
	var st unix.Stat_t
	err := unix.Fstatat(int(directory.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == unix.ENOENT {
		return nil
	}
	if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrState
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrState
	}
	defer unix.Close(fd)
	_, err = privateStat(fd)
	return err
}

// The driver applies these on every physical connection, including replacements
// after cancellation. Persistent and connection-local limits share one contract.
var statePragmas = []string{"busy_timeout=100", "page_size=4096", "journal_mode=DELETE", "synchronous=FULL", "secure_delete=ON", "max_page_count=65536", "journal_size_limit=1048576", "temp_store=MEMORY"}

func stateURI(path, vfs string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: url.Values{"vfs": {vfs}, "mode": {"rw"}, "_pragma": statePragmas}.Encode()}).String()
}
func newVerifiedVFS(f *os.File) (*verifiedVFS, error) {
	_, directory, err := openStateDirectory(f.Name())
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			directory.Close()
		}
	}()
	expected, err := privateStat(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	tls := libc.NewTLS()
	defer tls.Close()
	unixName, err := libc.CString("unix")
	if err != nil {
		return nil, ErrState
	}
	defer libc.Xfree(tls, unixName)
	base := sqlite3.Xsqlite3_vfs_find(tls, unixName)
	if base == 0 {
		return nil, ErrState
	}
	v := &verifiedVFS{name: fmt.Sprintf("jev-verified-%d", vfsSerial.Add(1)), expected: expected, directory: directory}
	v.ptr = libc.Xcalloc(tls, 1, types.Size_t(unsafe.Sizeof(sqlite3.Tsqlite3_vfs{})))
	if v.ptr == 0 {
		return nil, ErrState
	}
	copied := readNative[sqlite3.Tsqlite3_vfs](tls, base)
	v.baseOpen = copied.FxOpen
	copied.FpNext = 0
	copied.FzName, err = libc.CString(v.name)
	if err != nil {
		libc.Xfree(tls, v.ptr)
		return nil, ErrState
	}
	fn := verifiedOpen
	copied.FxOpen = *(*uintptr)(unsafe.Pointer(&fn))
	writeNative(tls, v.ptr, copied)
	verifiedFiles.Store(v.ptr, v)
	if sqlite3.Xsqlite3_vfs_register(tls, v.ptr, 0) != sqlite3.SQLITE_OK {
		v.close()
		owned = true
		return nil, ErrState
	}
	owned = true
	return v, nil
}
func (v *verifiedVFS) close() {
	v.directory.Close()
	tls := libc.NewTLS()
	defer tls.Close()
	sqlite3.Xsqlite3_vfs_unregister(tls, v.ptr)
	verifiedFiles.Delete(v.ptr)
	libc.Xfree(tls, readNative[sqlite3.Tsqlite3_vfs](tls, v.ptr).FzName)
	libc.Xfree(tls, v.ptr)
}
func verifiedOpen(tls *libc.TLS, vfs, name, file uintptr, flags int32, out uintptr) int32 {
	value, ok := verifiedFiles.Load(vfs)
	if !ok {
		return sqlite3.SQLITE_CANTOPEN
	}
	v := value.(*verifiedVFS)
	// Reject special files before Unix xOpen: a read-only FIFO open can block
	// before the descriptor check below ever runs. The anchored private directory
	// excludes other OS identities from racing this check (see documented limit).
	if !v.safeOpenName(name) {
		return sqlite3.SQLITE_CANTOPEN
	}
	call := *(*func(*libc.TLS, uintptr, uintptr, uintptr, int32, uintptr) int32)(unsafe.Pointer(&v.baseOpen))
	rc := call(tls, vfs, name, file, flags|sqlite3.SQLITE_OPEN_NOFOLLOW, out)
	if rc != sqlite3.SQLITE_OK {
		return rc
	}
	fd := readNative[sqlite3.TunixFile](tls, file).Fh
	st, err := privateDescriptorStat(int(fd), flags&sqlite3.SQLITE_OPEN_DELETEONCLOSE != 0)
	if err == nil && flags&sqlite3.SQLITE_OPEN_MAIN_DB != 0 && (st.Dev != v.expected.Dev || st.Ino != v.expected.Ino) {
		err = ErrState
	}
	if err == nil {
		return rc
	}
	methods := readNative[sqlite3.Tsqlite3_io_methods](tls, readNative[sqlite3.Tsqlite3_file](tls, file).FpMethods)
	closeFile := *(*func(*libc.TLS, uintptr) int32)(unsafe.Pointer(&methods.FxClose))
	closeFile(tls, file)
	libc.AssignPtrUintptr(file, 0)
	return sqlite3.SQLITE_CANTOPEN
}

// SQLite owns native allocations. Copy exactly one scalar ABI struct via its
// byte view; no Go pointer is retained by SQLite or converted to an integer.
func readNative[T any](tls *libc.TLS, address uintptr) *T {
	value := new(T)
	size := int(unsafe.Sizeof(*value))
	copy(unsafe.Slice((*byte)(unsafe.Pointer(value)), size), libc.GoBytes(address, size))
	return value
}
func writeNative[T any](tls *libc.TLS, address uintptr, value *T) {
	size := int(unsafe.Sizeof(*value))
	copy(libc.GoBytes(address, size), unsafe.Slice((*byte)(unsafe.Pointer(value)), size))
}

// Resolve ancestors once, then use only that canonical directory path. The leaf
// itself must not be a symlink. Retain its descriptor for every subsequent open.
// All ancestor names are protected by root/current-user ownership and either
// non-writable modes or sticky-directory rename protection.
func openStateDirectory(path string) (string, *os.File, error) {
	parent := filepath.Dir(path)
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(parent))
	if err != nil {
		return "", nil, ErrState
	}
	parent = filepath.Join(canonicalParent, filepath.Base(parent))
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, ErrState
	}
	dir := os.NewFile(uintptr(fd), parent)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		dir.Close()
		return "", nil, ErrState
	}
	for ancestor := filepath.Dir(parent); ; ancestor = filepath.Dir(ancestor) {
		var st unix.Stat_t
		if unix.Lstat(ancestor, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR ||
			(st.Uid != 0 && st.Uid != uint32(os.Geteuid())) ||
			(st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0) {
			dir.Close()
			return "", nil, ErrState
		}
		if ancestor == string(filepath.Separator) {
			break
		}
	}
	return filepath.Join(parent, filepath.Base(path)), dir, nil
}
func (v *verifiedVFS) safeOpenName(name uintptr) bool {
	var anchored, current unix.Stat_t
	if unix.Fstat(int(v.directory.Fd()), &anchored) != nil ||
		unix.Lstat(v.directory.Name(), &current) != nil ||
		current.Mode&unix.S_IFMT != unix.S_IFDIR || current.Mode&0077 != 0 ||
		current.Uid != uint32(os.Geteuid()) || current.Dev != anchored.Dev || current.Ino != anchored.Ino {
		return false
	}
	if name == 0 {
		return true
	} // unnamed SQLite temporary file, checked after open
	path := libc.GoString(name)
	if filepath.Dir(path) != v.directory.Name() {
		return false
	}
	var st unix.Stat_t
	err := unix.Fstatat(int(v.directory.Fd()), filepath.Base(path), &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == unix.ENOENT {
		return true
	}
	return err == nil && st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&0077 == 0 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1
}
