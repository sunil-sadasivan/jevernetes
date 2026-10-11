package safefile

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

var atomicGeneration = regexp.MustCompile(`^\.\.[0-9]{4}_[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}\.[0-9]+$`)

type deploymentInputsKey struct{}

// WithDeploymentInputs is an explicit controller deployment opt-in. Only the
// fixed policy/rules/credential paths in our manifests gain projection support.
func WithDeploymentInputs(ctx context.Context) context.Context {
	return context.WithValue(ctx, deploymentInputsKey{}, true)
}

func deploymentInput(path string) bool {
	switch path {
	case "/etc/jevernetes/policy.json", "/etc/jevernetes-reviewed/rules.json",
		"/var/run/jevernetes-secrets/jev-key", "/var/run/jevernetes-secrets/webhook-url",
		"/var/run/jevernetes-secrets/openai-key", "/var/run/jevernetes-secrets/anthropic-key":
		return true
	}
	return false
}

// ReadConfig leaves all other user paths under the ordinary no-alias contract.
func ReadConfig(ctx context.Context, path string, limit int64) ([]byte, error) {
	if enabled, _ := ctx.Value(deploymentInputsKey{}).(bool); enabled && deploymentInput(path) {
		return readProjected(ctx, path, limit)
	}
	return Read(ctx, path, limit)
}

// readProjected never follows a symlink with open. It interprets exactly two
// atomic-writer links, then opens a pinned generation directory and regular leaf
// with openat/O_NOFOLLOW. Rotation may yield the old or new complete file, or an
// error during generation removal; it cannot redirect an open outside the root.
// Mount ancestors are trusted, as for ordinary inputs. The deployment mounts
// these roots read-only; root/current-user directory ownership is required.
// Kubelet creates the volume root through emptyDir with mode 0777 (fsGroup
// adds setgid). Its read-only mount, not mode bits, is the write boundary.
// Timestamp directories are 0755 (plus fsGroup bits), never world-writable.
func readProjected(ctx context.Context, path string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	if name == "." || strings.HasPrefix(name, "..") {
		return nil, ErrUnsafe
	}
	root, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	safeDir := func(fd int, generation bool) bool {
		var st unix.Stat_t
		return unix.Fstat(fd, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR && (st.Uid == 0 || st.Uid == uint32(os.Geteuid())) && (!generation || st.Mode&0002 == 0)
	}
	if !safeDir(root, false) {
		return nil, ErrUnsafe
	}
	link := func(fd int, name string) (string, error) {
		b := make([]byte, 4096)
		n, e := unix.Readlinkat(fd, name, b)
		if e != nil {
			return "", e
		}
		if n == len(b) {
			return "", ErrUnsafe
		}
		return string(b[:n]), nil
	}
	leaf, err := link(root, name)
	if err != nil || leaf != "..data/"+name {
		return nil, ErrUnsafe
	}
	generation, err := link(root, "..data")
	if err != nil || !atomicGeneration.MatchString(generation) {
		return nil, ErrUnsafe
	}
	dir, err := unix.Openat(root, generation, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	if !safeDir(dir, true) {
		return nil, ErrUnsafe
	}
	var before unix.Stat_t
	if unix.Fstatat(dir, name, &before, unix.AT_SYMLINK_NOFOLLOW) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 {
		return nil, ErrUnsafe
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	raw := os.NewFile(uintptr(fd), name)
	f := &File{File: raw, done: make(chan struct{})}
	f.stop = context.AfterFunc(ctx, func() { raw.Close(); close(f.done) })
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Dev != before.Dev || st.Ino != before.Ino || limit < 0 || st.Size > limit {
		return nil, ErrUnsafe
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrUnsafe
	}
	return b, ctx.Err()
}
