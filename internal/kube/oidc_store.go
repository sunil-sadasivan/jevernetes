package kube

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
)

// Use client-go's .lock convention, holding it across reread, refresh and atomic
// replacement. Cooperating processes cannot consume the same rotating token.
// Noncooperating edits are merged or rejected on reread; same-UID replacement
// racing the final rename is outside the ownership boundary (as for state).
type oidcStore struct{ path, user string }

func (*oidcStore) Persist(map[string]string) error { return errOIDC } // Never allow context-free persistence.
type oidcTransaction struct {
	store *oidcStore
	dir   *os.File
	lock  string
}

func (s *oidcStore) begin(ctx context.Context) (*oidcTransaction, error) {
	parent := filepath.Dir(s.path)
	canonical, err := filepath.EvalSymlinks(filepath.Dir(parent))
	if err != nil {
		return nil, errOIDC
	}
	parent = filepath.Join(canonical, filepath.Base(parent))
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errOIDC
	}
	dir := os.NewFile(uintptr(fd), parent)
	good := false
	defer func() {
		if !good {
			dir.Close()
		}
	}()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		return nil, errOIDC
	}
	for p := filepath.Dir(parent); ; p = filepath.Dir(p) {
		if unix.Lstat(p, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0) {
			return nil, errOIDC
		}
		if p == string(filepath.Separator) {
			break
		}
	}
	tx := &oidcTransaction{store: s, dir: dir, lock: filepath.Base(s.path) + ".lock"}
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lock, e := unix.Openat(fd, tx.lock, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if e == nil {
			unix.Close(lock)
			good = true
			return tx, nil
		}
		if e != unix.EEXIST {
			return nil, errOIDC
		}
		// A preexisting lock is never opened or removed, including symlinks/FIFOs.
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (tx *oidcTransaction) close() { unix.Unlinkat(int(tx.dir.Fd()), tx.lock, 0); tx.dir.Close() }
func (tx *oidcTransaction) read() (*api.Config, map[string]string, []byte, error) {
	fd := int(tx.dir.Fd())
	var anchored, current unix.Stat_t
	if unix.Fstat(fd, &anchored) != nil || unix.Lstat(tx.dir.Name(), &current) != nil || anchored.Dev != current.Dev || anchored.Ino != current.Ino || current.Mode&unix.S_IFMT != unix.S_IFDIR || current.Mode&0077 != 0 || current.Uid != uint32(os.Geteuid()) {
		return nil, nil, nil, errOIDC
	}
	name := filepath.Base(tx.store.path)
	var before unix.Stat_t
	if unix.Fstatat(fd, name, &before, unix.AT_SYMLINK_NOFOLLOW) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, nil, nil, errOIDC
	}
	f, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, nil, nil, errOIDC
	}
	file := os.NewFile(uintptr(f), name)
	defer file.Close()
	var st unix.Stat_t
	if unix.Fstat(f, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Mode&0200 == 0 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Ino != before.Ino || st.Dev != before.Dev || st.Size > 1<<20 {
		return nil, nil, nil, errOIDC
	}
	b, e := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return nil, nil, nil, errOIDC
	}
	c, e := clientcmd.Load(b)
	if e != nil {
		return nil, nil, nil, errOIDC
	}
	auth := c.AuthInfos[tx.store.user]
	if auth == nil || auth.AuthProvider == nil || auth.AuthProvider.Name != "oidc" {
		return nil, nil, nil, errOIDC
	}
	return c, auth.AuthProvider.Config, b, nil
}
func (tx *oidcTransaction) persist(previous, next map[string]string) error {
	c, current, before, err := tx.read()
	if err != nil || !maps.Equal(previous, current) {
		return errOIDC
	}
	// Reparse the latest file so unrelated concurrent changes survive.
	c.AuthInfos[tx.store.user].AuthProvider.Config = maps.Clone(next)
	data, err := clientcmd.Write(*c)
	if err != nil || len(data) > 1<<20 {
		return errOIDC
	}
	fd := int(tx.dir.Fd())
	name := filepath.Base(tx.store.path)
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return errOIDC
	}
	temp := fmt.Sprintf(".oidc-%x", nonce)
	out, err := unix.Openat(fd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return errOIDC
	}
	f := os.NewFile(uintptr(out), temp)
	defer f.Close()
	defer unix.Unlinkat(fd, temp, 0)
	if f.Chmod(0600) != nil {
		return errOIDC
	}
	if _, err = f.Write(data); err != nil {
		return errOIDC
	}
	if f.Sync() != nil || f.Close() != nil {
		return errOIDC
	}
	// Catch noncooperating edits during serialization/write, including unsafe
	// substitutions. No rename ever follows the destination leaf.
	_, _, latest, err := tx.read()
	if err != nil || !bytes.Equal(before, latest) {
		return errOIDC
	}
	if unix.Renameat(fd, temp, fd, name) != nil || tx.dir.Sync() != nil {
		return errOIDC
	}
	return nil
}
