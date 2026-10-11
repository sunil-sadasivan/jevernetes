package safefile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProjectedDeploymentInputs(t *testing.T) {
	mounts := map[string]bool{}
	for _, manifest := range []string{"../../deploy/base/resources.json", "../../deploy/reviewed-rules.patch.json"} {
		raw, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"--projected-inputs"`) {
			t.Fatal("deployment does not opt in to projection reads")
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		paths := map[string]bool{}
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if path, ok := x["mountPath"].(string); ok {
					mounts[path], _ = x["readOnly"].(bool)
				}
				for _, v := range x {
					walk(v)
				}
			case []any:
				for _, v := range x {
					walk(v)
				}
			case string:
				switch filepath.Dir(x) {
				case "/etc/jevernetes", "/etc/jevernetes-reviewed", "/var/run/jevernetes-secrets":
					paths[x] = true
				}
			}
		}
		walk(doc)
		if len(paths) == 0 {
			t.Fatal("no deployment input paths")
		}
		for path := range paths {
			t.Run(path, func(t *testing.T) {
				if !mounts[filepath.Dir(path)] {
					t.Fatal("deployment input is not mounted read-only")
				}
				if !deploymentInput(path) {
					t.Fatal("deployment input missing from fixed allowlist")
				}
				root := t.TempDir()
				if err := os.Chmod(root, os.ModeSetgid|0777); err != nil {
					t.Fatal(err)
				}
				generation := "..2026_10_11_12_00_00.123456789"
				if err := os.Mkdir(filepath.Join(root, generation), 0755); err != nil {
					t.Fatal(err)
				}
				name := filepath.Base(path)
				if err := os.WriteFile(filepath.Join(root, generation, name), []byte("synthetic"), 0440); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(generation, filepath.Join(root, "..data")); err != nil {
					t.Fatal(err)
				}
				leaf := filepath.Join(root, name)
				if err := os.Symlink("..data/"+name, leaf); err != nil {
					t.Fatal(err)
				}
				if _, err := Read(context.Background(), leaf, 1024); err == nil {
					t.Fatal("ordinary user-path protections weakened")
				}
				raw, err := readProjected(context.Background(), leaf, 1024)
				if err != nil || string(raw) != "synthetic" {
					t.Fatalf("deployment input unreadable: %v", err)
				}
			})
		}
	}
}

func projectionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	generation := filepath.Join(root, "..2026_10_11_12_00_00.123456789")
	if err := os.Mkdir(generation, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "input"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(generation), filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/input", filepath.Join(root, "input")); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "input"), generation
}

func TestProjectedRejectsAliasesAndEscapes(t *testing.T) {
	for _, kind := range []string{"leaf-absolute", "leaf-traversal", "data-absolute", "data-traversal", "generation-link", "regular-link", "hardlink", "fifo", "directory", "oversize", "world-writable", "cancelled", "ordinary-opt-in"} {
		t.Run(kind, func(t *testing.T) {
			path, gen := projectionFixture(t)
			root := filepath.Dir(path)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			replace := func(path, target string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			limit := int64(1024)
			ctx := context.Background()
			switch kind {
			case "leaf-absolute":
				replace(path, outside)
			case "leaf-traversal":
				replace(path, "..data/../input")
			case "data-absolute":
				replace(filepath.Join(root, "..data"), filepath.Dir(outside))
			case "data-traversal":
				replace(filepath.Join(root, "..data"), "../"+filepath.Base(gen))
			case "generation-link":
				if err := os.Rename(gen, gen+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(gen+"-moved", gen); err != nil {
					t.Fatal(err)
				}
			case "regular-link":
				replace(filepath.Join(gen, "input"), outside)
			case "hardlink":
				if err := os.Link(filepath.Join(gen, "input"), filepath.Join(gen, "alias")); err != nil {
					t.Fatal(err)
				}
			case "fifo", "directory":
				if err := os.Remove(filepath.Join(gen, "input")); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "fifo" {
					err = unix.Mkfifo(filepath.Join(gen, "input"), 0600)
				} else {
					err = os.Mkdir(filepath.Join(gen, "input"), 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "oversize":
				limit = 2
			case "world-writable":
				if err := os.Chmod(gen, 0777); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "ordinary-opt-in":
				if _, err := ReadConfig(WithDeploymentInputs(ctx), path, limit); err == nil {
					t.Fatal("opt-in allowed arbitrary path")
				}
				return
			}
			if _, err := readProjected(ctx, path, limit); err == nil {
				t.Fatal("unsafe projection accepted")
			}
		})
	}
}

func TestProjectedRotationRaces(t *testing.T) {
	path, gen := projectionFixture(t)
	root := filepath.Dir(path)
	second := filepath.Join(root, "..2026_10_11_12_01_00.987654321")
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "input"), []byte("rotated"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			target := filepath.Base(gen)
			if i%3 == 1 {
				target = filepath.Base(second)
			}
			if i%3 == 2 {
				target = filepath.Dir(outside)
			}
			tmp := filepath.Join(root, "..data_tmp")
			if err := os.Symlink(target, tmp); err != nil {
				errs <- err
				return
			}
			if err := os.Rename(tmp, filepath.Join(root, "..data")); err != nil {
				errs <- err
				return
			}
		}
	}()
	for i := 0; i < 500; i++ {
		raw, err := readProjected(context.Background(), path, 1024)
		if err == nil && string(raw) != "synthetic" && string(raw) != "rotated" {
			t.Fatal("escaped generation")
		}
	}
	<-done
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	// Replacement of an in-generation leaf cannot turn validation into a follow.
	if err := os.Remove(filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(gen), filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	done = make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			target := filepath.Join(gen, "input")
			tmp := filepath.Join(gen, "replacement")
			if i%2 == 0 {
				if err := os.Symlink(outside, tmp); err != nil {
					errs <- err
					return
				}
			} else {
				if err := os.WriteFile(tmp, []byte("synthetic"), 0600); err != nil {
					errs <- err
					return
				}
			}
			if err := os.Rename(tmp, target); err != nil {
				errs <- err
				return
			}
		}
	}()
	for i := 0; i < 300; i++ {
		raw, err := readProjected(context.Background(), path, 1024)
		if err == nil && string(raw) != "synthetic" && string(raw) != "rotated" {
			t.Fatal("followed replacement leaf")
		}
	}
	<-done
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}
