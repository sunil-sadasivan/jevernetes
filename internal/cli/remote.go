package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	core "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/dashboard"
	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
	"github.com/sunil-sadasivan/jevernetes/internal/investigation"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"github.com/sunil-sadasivan/jevernetes/internal/output"
	"github.com/sunil-sadasivan/jevernetes/internal/report"
)

func remoteClient(ctx context.Context, k kube.Config, port int) (*inspect.Client, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	cfg, err := kube.LoadContext(ctx, k)
	if err != nil {
		return nil, err
	}
	httpClient, err := kube.HTTPClientFor(cfg)
	if err != nil {
		return nil, errors.New("Kubernetes transport unavailable")
	}
	api, err := core.NewForConfigAndClient(cfg, httpClient)
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
		client, err := remoteClient(c.Context(), k, port)
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
					if err := json.NewEncoder(output.Writer{Context: c.Context(), Output: c.OutOrStdout()}).Encode(map[string]any{"sampled_at": time.Now().Unix(), "stale": true, "error": "controller inspection unavailable"}); err != nil {
						return err
					}
				} else {
					if _, err := fmt.Fprintln(output.Writer{Context: c.Context(), Output: c.ErrOrStderr()}, "Controller sample unavailable; previous output is stale"); err != nil {
						return err
					}
				}
			} else {
				if o.output != "" {
					if err := report.Write(o.output, value); err != nil {
						return err
					}
				}
				if o.json {
					if json.NewEncoder(output.Writer{Context: c.Context(), Output: c.OutOrStdout()}).Encode(value) != nil {
						return errors.New("cannot write inspection output")
					}
				} else {
					raw, _ := json.MarshalIndent(value, "", "  ")
					text := string(raw)
					if len(text) > 32768 {
						text = text[:32768] + "\n[output bounded]"
					}
					if _, err := fmt.Fprintln(output.Writer{Context: c.Context(), Output: c.OutOrStdout()}, text); err != nil {
						return err
					}
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
	var listen, path, binary, reportsDir string
	var inspectionOnly bool
	var dashboardPort int
	c := &cobra.Command{Use: "dashboard", Short: "Serve the native Go investigation and controller dashboards", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if c.Flags().Changed("port") {
			if c.Flags().Changed("listen") || dashboardPort < 1 || dashboardPort > 65535 {
				return errors.New("--port must be 1–65535 and cannot combine with --listen")
			}
			listen = fmt.Sprintf("127.0.0.1:%d", dashboardPort)
		}
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
			raw, err := readBounded(c.Context(), path, 64<<20)
			if err != nil {
				return err
			}
			d.Report, err = dashboard.LoadReport(raw)
			if err != nil {
				return err
			}
		}
		if k.Namespace != "" {
			client, err := remoteClient(c.Context(), k, port)
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
		if _, err := fmt.Fprintf(output.Writer{Context: c.Context(), Output: c.ErrOrStderr()}, "Dashboard: http://%s\n", l.Addr().String()); err != nil {
			return err
		}
		if inspectionOnly {
			return d.Serve(c.Context(), l)
		}
		app, err := investigation.New(c.Context(), reportsDir, o.offline)
		if err != nil {
			return err
		}
		defer app.Close()
		if path != "" {
			if err = app.Import(path); err != nil {
				return err
			}
		}
		app.Scan = scanDashboard
		app.Context = kube.FetchContext
		full := app.Handler(l.Addr().String())
		compact := d.Handler(l.Addr().String())
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/controller/") {
				r = r.Clone(r.Context())
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/controller")
				compact.ServeHTTP(w, r)
				return
			}
			full.ServeHTTP(w, r)
		})
		return inspect.ServeWithWriteTimeout(c.Context(), l, handler, true, 30*time.Second)
	}}
	f := c.Flags()
	f.IntVar(&dashboardPort, "port", 8765, "Loopback dashboard port (alias for --listen)")
	f.StringVar(&reportsDir, "reports-dir", ".runs", "Private report and human review directory")
	f.BoolVar(&inspectionOnly, "inspection-only", false, "Serve the compact read-only controller inspection dashboard")
	f.StringVar(&listen, "listen", "127.0.0.1:8765", "Numeric loopback address")
	f.StringVar(&path, "report", "", "Local schema-1 or schema-2 report to inspect")
	f.StringVar(&k.Context, "controller-context", "", "Fixed controller context")
	f.StringVar(&k.Namespace, "controller-namespace", "", "Fixed controller namespace")
	f.StringVar(&k.Pod, "controller-pod", "", "Fixed exact pod")
	f.StringVar(&k.Selector, "controller-selector", "", "Fixed selector")
	f.IntVar(&port, "controller-port", 9091, "Fixed controller inspection port")
	f.IntVar(&interval, "controller-interval", 5, "Poll/cache interval in seconds")
	f.StringVar(&binary, "controller-binary", "", "Unsupported: no subprocess bridge is needed")
	return c
}
