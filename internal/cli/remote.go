package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/spf13/cobra"
	core "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/dashboard"
	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"github.com/sunil-sadasivan/jevernetes/internal/report"
)

func remoteClient(k kube.Config, port int) (*inspect.Client, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	cfg, err := kube.Load(k)
	if err != nil {
		return nil, err
	}
	api, err := core.NewForConfig(cfg)
	if err != nil {
		return nil, errors.New("Kubernetes client unavailable")
	}
	return &inspect.Client{API: api, Forward: kube.SPDYForwarder{Config: cfg}, Config: k, Port: port}, nil
}
func remoteCommand(o *options) *cobra.Command {
	k := kube.Config{MaxBytes: 32 << 20, MaxStreams: 64}
	var port, interval int
	var watch bool
	var id string
	c := &cobra.Command{Use: "remote", Short: "Inspect one controller through Kubernetes port-forward", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if k.Namespace == "" {
			return errors.New("remote requires --namespace")
		}
		if id != "" && !controller.ValidID(id) {
			return errors.New("invalid incident id")
		}
		if port < 1 || port > 65535 || interval < 2 || interval > 3600 {
			return errors.New("invalid remote port or interval")
		}
		client, err := remoteClient(k, port)
		if err != nil {
			return err
		}
		for {
			value, err := client.Sample(c.Context(), id)
			if err != nil {
				if c.Context().Err() != nil {
					return c.Context().Err()
				}
				if !watch {
					return err
				}
				if o.json {
					_ = json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"sampled_at": time.Now().Unix(), "stale": true, "error": "controller inspection unavailable"})
				} else {
					fmt.Fprintln(c.ErrOrStderr(), "Controller sample unavailable; previous output is stale")
				}
			} else {
				if o.output != "" {
					if err := report.Write(o.output, value); err != nil {
						return err
					}
				}
				if o.json {
					if json.NewEncoder(c.OutOrStdout()).Encode(value) != nil {
						return errors.New("cannot write inspection output")
					}
				} else {
					raw, _ := json.MarshalIndent(value, "", "  ")
					text := string(raw)
					if len(text) > 32768 {
						text = text[:32768] + "\n[output bounded]"
					}
					fmt.Fprintln(c.OutOrStdout(), text)
				}
			}
			if !watch {
				return nil
			}
			timer := time.NewTimer(time.Duration(interval) * time.Second)
			select {
			case <-c.Context().Done():
				timer.Stop()
				return c.Context().Err()
			case <-timer.C:
			}
		}
	}}
	f := c.Flags()
	f.StringVar(&k.Context, "context", "", "Kubeconfig context")
	f.StringVar(&k.Namespace, "namespace", "", "Required controller namespace")
	f.StringVar(&k.Pod, "pod", "", "Exact running controller pod")
	f.StringVar(&k.Selector, "selector", "", "Selector (default app=jevernetes); must select exactly one")
	f.IntVar(&port, "port", 9091, "Controller loopback inspection port")
	f.IntVar(&interval, "interval", 5, "Watch interval seconds (2–3600)")
	f.BoolVar(&watch, "watch", false, "Continuously inspect; mark failures stale")
	f.StringVar(&id, "incident", "", "Exact 64-hex incident ID")
	return c
}
func dashboardCommand(o *options) *cobra.Command {
	k := kube.Config{MaxBytes: 32 << 20, MaxStreams: 64}
	var port, interval int
	var listen, path, binary string
	c := &cobra.Command{Use: "dashboard", Short: "Serve a read-only loopback report and controller dashboard", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		host, _, err := net.SplitHostPort(listen)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("dashboard requires numeric loopback listen address")
		}
		if port < 1 || port > 65535 || interval < 2 || interval > 3600 {
			return errors.New("invalid controller port or interval")
		}
		if binary != "" {
			return errors.New("--controller-binary is unsupported: dashboard inspection runs inside the Go process")
		}
		if (k.Context == "") != (k.Namespace == "") {
			return errors.New("controller context and namespace must be configured together")
		}
		if k.Namespace == "" && (k.Pod != "" || k.Selector != "") {
			return errors.New("controller target requires context and namespace")
		}
		d := &dashboard.Dashboard{Interval: time.Duration(interval) * time.Second}
		if path != "" {
			raw, err := readBounded(path, 32<<20)
			if err != nil {
				return err
			}
			var value map[string]any
			if provider.Strict(raw, &value) != nil || value["schema_version"] != float64(2) {
				return errors.New("dashboard requires schema-2 report")
			} // Only bounded event previews reach the browser.
			if events, ok := value["events"].([]any); ok && len(events) > 200 {
				value["events"] = events[len(events)-200:]
			}
			d.Report = func() any { return value }
		}
		if k.Namespace != "" {
			client, err := remoteClient(k, port)
			if err != nil {
				return err
			}
			d.Remote = client
		}
		l, err := net.Listen("tcp", listen)
		if err != nil {
			return errors.New("dashboard listener unavailable")
		}
		defer l.Close()
		fmt.Fprintf(c.ErrOrStderr(), "Dashboard: http://%s\n", l.Addr().String())
		return d.Serve(c.Context(), l)
	}}
	f := c.Flags()
	f.StringVar(&listen, "listen", "127.0.0.1:8765", "Numeric loopback address")
	f.StringVar(&path, "report", "", "Local schema-2 report to inspect")
	f.StringVar(&k.Context, "controller-context", "", "Fixed controller context")
	f.StringVar(&k.Namespace, "controller-namespace", "", "Fixed controller namespace")
	f.StringVar(&k.Pod, "controller-pod", "", "Fixed exact pod")
	f.StringVar(&k.Selector, "controller-selector", "", "Fixed selector")
	f.IntVar(&port, "controller-port", 9091, "Fixed controller inspection port")
	f.IntVar(&interval, "controller-interval", 5, "Poll/cache interval in seconds")
	f.StringVar(&binary, "controller-binary", "", "Unsupported: no subprocess bridge is needed")
	return c
}
