package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Translate a fixed browser contract to known CLI options; uploaded names never
// become flags, and the browser cannot select executables, output paths or URLs.
func scanDashboard(ctx context.Context, p map[string]any, paths []string, update func(map[string]any)) (map[string]any, error) {
	kind, _ := p["kind"].(string)
	args := []string{"k8s"}
	if kind == "files" {
		args = []string{"files"}
	}
	if p["offline"] == true {
		args = append(args, "--offline")
	}
	if p["grouping"] == false {
		args = append(args, "--no-grouping")
	}
	for _, key := range []string{"max_batches", "retain_events", "max_cost"} {
		if value, ok := p[key]; ok {
			args = append(args, "--"+strings.ReplaceAll(key, "_", "-"), fmt.Sprint(value))
		}
	}
	if kind == "kubernetes" {
		if p["live"] == true {
			args = append(args, "--follow")
		}
		for _, key := range []string{"context", "namespace", "selector", "since", "tail", "max_streams"} {
			if value, ok := p[key]; ok && value != "" {
				args = append(args, "--"+strings.ReplaceAll(key, "_", "-"), fmt.Sprint(value))
			}
		}
	} else {
		args = append(args, "--")
		args = append(args, paths...)
	}
	if path, ok := p["_review_rules"].(string); ok {
		args = append(args[:1], append([]string{"--rules-file", path}, args[1:]...)...)
	}
	names := map[string]string{}
	for _, path := range paths {
		names[path] = filepath.Base(path)
	}
	var final map[string]any
	cmd := newCommand(strings.NewReader(""), io.Discard, io.Discard, runtimeDeps{sourceNames: names, quiet: true, update: update, finished: func(v map[string]any) { final = v }})
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	var ee exitError
	if errors.As(err, &ee) && ee.code == 2 {
		err = nil
	}
	return final, err
}
