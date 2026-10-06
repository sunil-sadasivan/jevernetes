package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestDeploymentStateCompatibility(t *testing.T) {
	data, err := os.ReadFile("../../deploy/base/resources.json")
	if err != nil {
		t.Fatal(err)
	}
	var resources struct{ Items []json.RawMessage }
	if err := json.Unmarshal(data, &resources); err != nil {
		t.Fatal(err)
	}
	var deployments []appsv1.Deployment
	for _, item := range resources.Items {
		var candidate appsv1.Deployment
		if err := json.Unmarshal(item, &candidate); err == nil && candidate.Kind == "Deployment" {
			deployments = append(deployments, candidate)
		}
	}
	if len(deployments) != 1 {
		t.Fatal("expected one Deployment")
	}
	deployment := deployments[0]
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatal("migration requires single-replica Recreate")
	}
	pod := deployment.Spec.Template.Spec
	sc := pod.SecurityContext
	if sc == nil || sc.FSGroup == nil || *sc.FSGroup != 65532 || sc.RunAsUser == nil || *sc.RunAsUser != 65532 || sc.RunAsGroup == nil || *sc.RunAsGroup != 65532 || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("pod identity/fsGroup/seccomp contract")
	}
	if len(pod.Containers) != 1 || len(pod.InitContainers) != 1 {
		t.Fatal("expected controller and one init container")
	}
	main, init := pod.Containers[0], pod.InitContainers[0]
	if init.Image == "" || init.Image != main.Image || init.ImagePullPolicy != main.ImagePullPolicy {
		t.Fatal("init and main image identity mismatch")
	}
	for _, c := range []corev1.Container{main, init} {
		s := c.SecurityContext
		if s == nil || s.Privileged != nil && *s.Privileged || s.AllowPrivilegeEscalation == nil || *s.AllowPrivilegeEscalation || s.ReadOnlyRootFilesystem == nil || !*s.ReadOnlyRootFilesystem || s.Capabilities == nil || !reflect.DeepEqual(s.Capabilities.Drop, []corev1.Capability{"ALL"}) || len(s.Capabilities.Add) != 0 {
			t.Fatalf("unsafe container: %s", c.Name)
		}
	}
	isc := init.SecurityContext
	if isc.RunAsUser == nil || *isc.RunAsUser != 65532 || isc.RunAsGroup == nil || *isc.RunAsGroup != 65532 || isc.RunAsNonRoot == nil || !*isc.RunAsNonRoot || isc.SeccompProfile == nil || isc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("init identity/seccomp contract")
	}
	if !reflect.DeepEqual(init.Command, []string{"/usr/local/bin/jevernetes"}) || !reflect.DeepEqual(init.Args, []string{"prepare-state-volume", "/state-volume"}) {
		t.Fatal("preparation must execute binary directly, overriding image entrypoint")
	}
	if !reflect.DeepEqual(init.VolumeMounts, []corev1.VolumeMount{{Name: "state", MountPath: "/state-volume"}}) || len(init.Env) != 0 || len(init.EnvFrom) != 0 {
		t.Fatal("init needs only volume root, no provider environment")
	}
	found := false
	for _, mount := range main.VolumeMounts {
		if mount.Name != "state" {
			continue
		}
		found = true
		if mount.MountPath != "/var/lib/jevernetes" || mount.SubPath != "private" || mount.SubPathExpr != "" || mount.ReadOnly {
			t.Fatal("main must mount private directly at state directory")
		}
	}
	if !found {
		t.Fatal("missing state mount")
	}
	found = false
	for i, arg := range main.Args {
		if arg == "--state" && i+1 < len(main.Args) && main.Args[i+1] == "/var/lib/jevernetes/state.db" {
			found = true
		}
	}
	if !found {
		t.Fatal("wrong state path")
	}
	found = false
	for _, v := range pod.Volumes {
		if v.Name == "state" && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "controller-state" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing state PVC")
	}

	// Preserve the original regression independently of manifest assertions:
	// even current-user ownership cannot rescue fsGroup's group-accessible root.
	root := privateTestDir(t)
	mustVolume(t, os.Chmod(root, 0770))
	if s, err := Open(filepath.Join(root, "state.db")); err == nil {
		s.Close()
		t.Fatal("fsGroup/direct-mount state accepted")
	}
	mustVolume(t, PrepareVolume(root))
	assertVolumeMode(t, filepath.Join(root, "private"), 0700)
	// Model the main container's direct bind mount under protected image ancestry.
	mounted := filepath.Join(privateTestDir(t), "mounted")
	mustVolume(t, os.Rename(filepath.Join(root, "private"), mounted))
	s, err := Open(filepath.Join(mounted, "state.db"))
	mustVolume(t, err)
	s.Close()
}

func mustVolume(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func assertVolumeMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	mustVolume(t, err)
	if st.Mode().Perm() != mode || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("wrong mode for %s: %v", filepath.Base(path), st.Mode())
	}
	var native unix.Stat_t
	mustVolume(t, unix.Stat(path, &native))
	if native.Uid != uint32(os.Geteuid()) {
		t.Fatal("wrong owner")
	}
}
func writeVolume(t *testing.T, path, data string) {
	t.Helper()
	mustVolume(t, os.WriteFile(path, []byte(data), 0600))
}

func TestPrepareVolumeCleanAndModes(t *testing.T) {
	root := privateTestDir(t)
	mustVolume(t, os.Chmod(root, 0770|os.ModeSetgid))
	mustVolume(t, os.Mkdir(filepath.Join(root, "lost+found"), 0700))
	for i := 0; i < 2; i++ {
		mustVolume(t, PrepareVolume(root))
		assertVolumeMode(t, filepath.Join(root, "private"), 0700)
	}
	state := filepath.Join(root, "private")
	writeVolume(t, filepath.Join(state, "state.db"), "synthetic state")
	for _, name := range volumeFiles {
		writeVolume(t, filepath.Join(state, name), name)
		mustVolume(t, os.Chmod(filepath.Join(state, name), 0660))
	}
	mustVolume(t, os.Chmod(state, 0770|os.ModeSetgid))
	mustVolume(t, PrepareVolume(root))
	assertVolumeMode(t, state, 0700)
	for _, name := range volumeFiles {
		assertVolumeMode(t, filepath.Join(state, name), 0600)
	}
	assertVolumeMode(t, filepath.Join(root, ".prepare.lock"), 0600)
}

func TestPrepareVolumeLegacy(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "empty"}[empty], func(t *testing.T) {
			root := privateTestDir(t)
			if empty {
				mustVolume(t, os.Mkdir(filepath.Join(root, "private"), 0770))
			}
			for _, name := range volumeFiles {
				writeVolume(t, filepath.Join(root, name), "synthetic "+name)
				mustVolume(t, os.Chmod(filepath.Join(root, name), 0660))
			}
			mustVolume(t, os.Chmod(root, 0770))
			mustVolume(t, PrepareVolume(root))
			mustVolume(t, PrepareVolume(root))
			for _, name := range volumeFiles {
				data, err := os.ReadFile(filepath.Join(root, "private", name))
				mustVolume(t, err)
				if string(data) != "synthetic "+name {
					t.Fatal("migration changed bytes", name)
				}
				assertVolumeMode(t, filepath.Join(root, "private", name), 0600)
				if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatal("legacy artifact remains", name, err)
				}
			}
		})
	}
}

func TestPrepareVolumeRejectsConflicts(t *testing.T) {
	for _, kind := range []string{"two-databases", "private-orphan", "legacy-orphan", "unknown-private", "unknown-legacy", "staging", "symlink-private", "symlink-db", "hardlink-db", "fifo-db", "foreign-owner", "active-writer", "active-preparer"} {
		t.Run(kind, func(t *testing.T) {
			root := privateTestDir(t)
			db := filepath.Join(root, "state.db")
			writeVolume(t, db, "legacy database")
			private := filepath.Join(root, "private")
			switch kind {
			case "two-databases", "private-orphan", "unknown-private":
				mustVolume(t, os.Mkdir(private, 0700))
				name := "state.db"
				if kind == "private-orphan" {
					name = "state.db-wal"
				}
				if kind == "unknown-private" {
					name = "unexpected"
				}
				writeVolume(t, filepath.Join(private, name), "destination")
			case "legacy-orphan":
				mustVolume(t, os.Remove(db))
				writeVolume(t, db+"-wal", "orphan")
			case "unknown-legacy":
				writeVolume(t, db+"-unexpected", "unknown")
			case "staging":
				mustVolume(t, os.Mkdir(filepath.Join(root, volumeStaging), 0700))
			case "symlink-private":
				mustVolume(t, os.Symlink(privateTestDir(t), private))
			case "symlink-db":
				mustVolume(t, os.Remove(db))
				target := filepath.Join(privateTestDir(t), "target")
				writeVolume(t, target, "untouched")
				mustVolume(t, os.Symlink(target, db))
			case "hardlink-db":
				mustVolume(t, os.Link(db, filepath.Join(root, "alias")))
			case "fifo-db":
				mustVolume(t, os.Remove(db))
				mustVolume(t, unix.Mkfifo(db, 0600))
			case "foreign-owner":
				if os.Geteuid() != 0 {
					t.Skip("requires ownership capability")
				}
				mustVolume(t, os.Chown(db, 65533, 65533))
			case "active-writer", "active-preparer":
				name := "state.db.lock"
				if kind == "active-preparer" {
					name = ".prepare.lock"
				}
				f, err := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_RDWR, 0600)
				mustVolume(t, err)
				defer f.Close()
				mustVolume(t, unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB))
			}
			if err := PrepareVolume(root); !errors.Is(err, errVolume) {
				t.Fatal("unsafe layout accepted", err)
			}
			if kind != "legacy-orphan" && kind != "symlink-db" && kind != "fifo-db" {
				data, err := os.ReadFile(db)
				mustVolume(t, err)
				if string(data) != "legacy database" {
					t.Fatal("legacy bytes changed")
				}
			}
			if kind == "two-databases" {
				data, err := os.ReadFile(filepath.Join(private, "state.db"))
				mustVolume(t, err)
				if string(data) != "destination" {
					t.Fatal("destination overwritten")
				}
			}
		})
	}
}

func TestPrepareVolumeInterruptedMigration(t *testing.T) {
	for stop := 1; stop <= len(volumeFiles); stop++ {
		t.Run(volumeFiles[stop-1], func(t *testing.T) {
			root := privateTestDir(t)
			for _, name := range volumeFiles {
				writeVolume(t, filepath.Join(root, name), name)
			}
			interrupted := errors.New("interrupted")
			moved := 0
			err := prepareVolume(root, func() error {
				moved++
				if moved == stop {
					return interrupted
				}
				return nil
			})
			if !errors.Is(err, interrupted) {
				t.Fatal(err)
			}
			if err := PrepareVolume(root); !errors.Is(err, errVolume) {
				t.Fatal("interrupted layout automatically resumed", err)
			}
			if _, err := os.Stat(filepath.Join(root, "private")); !os.IsNotExist(err) {
				t.Fatal("partial state published", err)
			}
			for i, name := range volumeFiles {
				dir := root
				if i < stop {
					dir = filepath.Join(root, volumeStaging)
				}
				data, err := os.ReadFile(filepath.Join(dir, name))
				mustVolume(t, err)
				if !bytes.Equal(data, []byte(name)) {
					t.Fatal("interrupted migration lost bytes")
				}
			}
		})
	}
}

func TestPrepareVolumeSQLiteRecovery(t *testing.T) {
	root := privateTestDir(t)
	path := filepath.Join(root, "state.db")
	s, err := Open(path)
	mustVolume(t, err)
	_, err = s.db.Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<2000) INSERT INTO seen SELECT printf('%064d',x),1000,'committed' FROM n")
	mustVolume(t, err)
	s.Close()
	binary, err := os.Executable()
	mustVolume(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestHotJournalRecoveryRetainsCommittedState$")
	cmd.Env = []string{"JEV_CRASH_PROBE=" + path}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v %s", err, out)
	}
	journal, err := os.ReadFile(path + "-journal")
	mustVolume(t, err)
	if len(journal) < 512 || journal[0] == 0 {
		t.Fatal("expected real hot journal")
	}
	before, err := os.ReadFile(path)
	mustVolume(t, err)
	mustVolume(t, os.Chmod(root, 0770))
	for _, name := range []string{"state.db", "state.db.lock", "state.db-journal"} {
		mustVolume(t, os.Chmod(filepath.Join(root, name), 0660))
	}
	mustVolume(t, PrepareVolume(root))
	for name, want := range map[string][]byte{"state.db": before, "state.db-journal": journal} {
		got, err := os.ReadFile(filepath.Join(root, "private", name))
		mustVolume(t, err)
		if !bytes.Equal(got, want) {
			t.Fatal("preparation must not run SQLite recovery", name)
		}
	}
	mounted := filepath.Join(privateTestDir(t), "mounted")
	mustVolume(t, os.Rename(filepath.Join(root, "private"), mounted))
	s, err = Open(filepath.Join(mounted, "state.db"))
	mustVolume(t, err)
	defer s.Close()
	var count int
	mustVolume(t, s.db.QueryRow("SELECT count(*) FROM seen WHERE decision='committed'").Scan(&count))
	if count != 2000 {
		t.Fatal("migration/recovery lost committed rows", count)
	}
	var integrity string
	mustVolume(t, s.db.QueryRow("PRAGMA integrity_check").Scan(&integrity))
	if integrity != "ok" {
		t.Fatal(integrity)
	}
}
