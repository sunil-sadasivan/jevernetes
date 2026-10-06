// Package cli validates options before opening inputs, configuration or credentials.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

var Version = "0.3.0-dev"

type exitError struct {
	code    int
	message string
}

func (e exitError) Error() string { return e.message }

type options struct {
	templateModel                                                                           string
	templateInput, templateOutput, templateCost, templateConfidence                         float64
	templateRequests, templateSamples, templateCapacity, templateTTL                        int
	offline, json, noGrouping, adaptive, budgeted                                           bool
	grouping, masking, risk, model, output, rules, templateProvider                         string
	batch, maxBatches, maxEvents, queue, retain, capacity, ttl, normalInterval, maxAttempts int
	maxCost, inputPrice, outputPrice                                                        float64
	reviewIDs                                                                               []string
	unsupported                                                                             map[string]*string
	k                                                                                       kube.Config
	state, policy, sink, listen                                                             string
	inspectPort                                                                             int
	hold, rescore                                                                           bool
}

func Main() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	code := Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if ctx.Err() != nil {
		return 130
	}
	return code
}
func Execute(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) int {
	cmd := New(in, out, stderr)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var ee exitError
	if errors.As(err, &ee) {
		if ee.message != "" {
			fmt.Fprintln(stderr, ee.message)
		}
		return ee.code
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return 130
	}
	message := err.Error()
	if strings.HasPrefix(message, "unknown command") {
		message = "unknown command; see --help"
	}
	fmt.Fprintln(stderr, message)
	return 1
}
func New(in io.Reader, out, stderr io.Writer) *cobra.Command {
	o := &options{unsupported: map[string]*string{}}
	root := &cobra.Command{Use: "jevernetes", Short: "Bounded, read-only Kubernetes log analysis", Version: Version, SilenceUsage: true, SilenceErrors: true}
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error { return exitError{2, "invalid command-line option; see --help"} })
	f := root.PersistentFlags()
	f.BoolVar(&o.offline, "offline", false, "Use deterministic local rules; no provider credentials or calls")
	f.BoolVar(&o.json, "json", false, "Write JSON to stdout")
	f.StringVar(&o.output, "output", "", "Atomically write a private JSON report")
	f.BoolVar(&o.noGrouping, "no-grouping", false, "Disable reuse")
	f.StringVar(&o.grouping, "grouping-strategy", "exact", "exact, off, drain, semantic (shadow only)")
	f.StringVar(&o.masking, "drain-masking", "strict", "strict or classic")
	f.IntVar(&o.capacity, "drain-capacity", 256, "Maximum resident templates (1–8192)")
	f.IntVar(&o.ttl, "drain-ttl", 300, "Non-sliding verdict TTL in seconds")
	f.BoolVar(&o.adaptive, "drain-adaptive", false, "Audit confirmed verdicts at bounded occurrence intervals")
	f.BoolVar(&o.budgeted, "drain-budgeted", false, "Use pessimistic request/cost admission")
	f.IntVar(&o.normalInterval, "drain-normal-interval", 1024, "Maximum adaptive audit gap (32–4096)")
	f.StringArrayVar(&o.reviewIDs, "drain-require-review", nil, "Veto reuse of an opaque template ID")
	f.IntVar(&o.maxAttempts, "drain-max-provider-attempts", 1500, "Absolute provider attempt limit")
	f.StringVar(&o.risk, "risk-provider", "typesafe", "typesafe, openai, anthropic")
	f.StringVar(&o.model, "model", "jev-latest", "Provider model identifier")
	f.StringVar(&o.rules, "template-rules", "", "Reviewed rules artifact version 2")
	f.StringVar(&o.templateProvider, "template-provider", "off", "off, openai or anthropic: shadow proposals / escalation review")
	f.IntVar(&o.batch, "batch-size", 8, "Microbatch maximum (1–64); fill deadline 350ms")
	f.IntVar(&o.maxBatches, "max-batches", 500, "Maximum provider batches")
	f.IntVar(&o.maxEvents, "max-events", 100000, "Global event cap (controller default: 0, unbounded)")
	f.IntVar(&o.queue, "queue-size", 1024, "Bounded analysis queue")
	f.IntVar(&o.retain, "retain-events", 2000, "Retained report events")
	f.Float64Var(&o.maxCost, "max-cost", 0.25, "Maximum estimated provider cost in USD")
	f.Float64Var(&o.inputPrice, "input-price", 0.042, "Input USD per million tokens")
	f.Float64Var(&o.outputPrice, "output-price", 0, "Output USD per million tokens")
	f.StringVar(&o.templateModel, "template-model", "", "Template model identifier")
	f.Float64Var(&o.templateInput, "template-input-price", 0, "Template input USD per million tokens")
	f.Float64Var(&o.templateOutput, "template-output-price", 0, "Template output USD per million tokens")
	f.Float64Var(&o.templateCost, "template-max-cost", .05, "Separate cumulative template reservation budget")
	f.Float64Var(&o.templateConfidence, "template-confidence", .85, "Minimum shadow proposal confidence")
	f.IntVar(&o.templateRequests, "template-max-requests", 10, "Maximum template provider attempts")
	f.IntVar(&o.templateSamples, "template-min-samples", 4, "First template review occurrence threshold")
	f.IntVar(&o.templateCapacity, "template-capacity", 64, "Resident template review candidates")
	f.IntVar(&o.templateTTL, "template-ttl", 300, "Template review state TTL seconds")
	for _, name := range []string{"drain-sample-rate", "drain-uncertain-ttl", "drain-max-cost"} {
		v := new(string)
		o.unsupported[name] = v
		f.StringVar(v, name, "", "Unsupported compatibility option; fails explicitly")
	}

	files := &cobra.Command{Use: "files PATH...", Short: "Analyze files, gzip members and '-' stdin", Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if err := o.validate(c, false); err != nil {
			return err
		}
		return o.run(c, args, in)
	}}
	files.Flags().Int64Var(&o.k.MaxBytes, "max-file-bytes", 32<<20, "Maximum decompressed bytes per input")
	root.AddCommand(files)
	k8s := &cobra.Command{Use: "k8s", Aliases: []string{"kubernetes"}, Short: "Read Kubernetes snapshot or live logs", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if err := o.validate(c, true); err != nil {
			return err
		}
		return o.run(c, nil, in)
	}}
	collectionFlags(k8s, &o.k)
	root.AddCommand(k8s)
	ctrl := &cobra.Command{Use: "controller", Short: "Durable read-only continuous controller", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o.k.InCluster = true
		o.k.Follow = true
		if !c.Flags().Changed("max-events") {
			o.maxEvents = 0
		}
		if o.k.Namespace == "" || o.state == "" {
			return errors.New("controller requires --namespace and --state")
		}
		if err := o.validate(c, true); err != nil {
			return err
		}
		return o.run(c, nil, in)
	}}
	collectionFlags(ctrl, &o.k)
	ctrl.Flags().StringVar(&o.state, "state", "", "Private SQLite state file")
	ctrl.Flags().StringVar(&o.policy, "policy", "", "Strict JSON policy file")
	ctrl.Flags().StringVar(&o.sink, "sink", "stdout", "stdout or webhook")
	ctrl.Flags().StringVar(&o.listen, "listen", "0.0.0.0:9090", "Health and metrics listener")
	ctrl.Flags().IntVar(&o.inspectPort, "inspect-port", 0, "Loopback inspection port, 0 disables")
	ctrl.Flags().BoolVar(&o.hold, "hold-after-stop", false, "Retain inspection and delivery after bounded collection stops")
	ctrl.Flags().IntVar(&o.ttl, "verdict-ttl", 300, "Verdict reuse TTL seconds (alias of --drain-ttl)")
	ctrl.Flags().BoolVar(&o.rescore, "rescore", false, "Bypass persisted verdicts")
	root.AddCommand(ctrl)
	root.AddCommand(&cobra.Command{
		Use: "prepare-state-volume PATH", Short: "Prepare a quiescent local controller volume (offline)",
		Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
			return controller.PrepareVolume(args[0])
		},
	})
	root.AddCommand(remoteCommand(o))
	root.AddCommand(dashboardCommand(o))
	for _, name := range []string{"search", "context", "review", "reviews", "tui"} {
		name := name
		root.AddCommand(&cobra.Command{Use: name, Short: "Unsupported compatibility command", DisableFlagParsing: true, RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%s is unsupported in the Go runtime; see docs/migration.md", name)
		}})
	}
	root.CompletionOptions.DisableDefaultCmd = true
	return root
}
func collectionFlags(c *cobra.Command, k *kube.Config) {
	f := c.Flags()
	f.StringVar(&k.Context, "context", "", "Kubeconfig context")
	f.BoolVar(&k.InCluster, "in-cluster", false, "Use mounted service account")
	f.StringVar(&k.Namespace, "namespace", "", "Namespace (empty: all)")
	f.StringVar(&k.Selector, "selector", "", "Label selector")
	f.StringVar(&k.Pod, "pod", "", "Exact pod name")
	f.BoolVarP(&k.Follow, "follow", "f", false, "Follow live streams")
	f.BoolVar(&k.Follow, "live", false, "Alias for --follow")
	f.BoolVar(&k.NoPrevious, "no-previous", false, "Exclude previous instances")
	f.Int64Var(&k.Tail, "tail", 500, "Tail lines per stream")
	f.Int64Var(&k.MaxBytes, "max-bytes", 32<<20, "Snapshot byte limit per stream")
	f.IntVar(&k.MaxStreams, "max-streams", 64, "Maximum live streams")
	f.String("since", "1h", "History window: integer seconds or s/m/h/d")
	f.Int64("duration", 0, "Stop collection after seconds (0: unbounded)")
}
func seconds(s string) (int64, error) {
	factor := int64(1)
	if len(s) > 0 {
		switch s[len(s)-1] {
		case 's':
			s = s[:len(s)-1]
		case 'm':
			factor = 60
			s = s[:len(s)-1]
		case 'h':
			factor = 3600
			s = s[:len(s)-1]
		case 'd':
			factor = 86400
			s = s[:len(s)-1]
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > 31536000/factor {
		return 0, errors.New("invalid --since duration")
	}
	return n * factor, nil
}
func (o *options) validate(c *cobra.Command, cluster bool) error {
	if o.noGrouping {
		o.grouping = "off"
	}
	if o.grouping == "learned" {
		o.grouping = "semantic"
	}

	if o.risk == "jev" {
		o.risk = "typesafe"
	}
	if o.rules != "" && o.grouping != "semantic" {
		return errors.New("reviewed rules require semantic grouping")
	}
	for name := range o.unsupported {
		if c.Flags().Changed(name) {
			return fmt.Errorf("--%s is unsupported in the Go runtime; see docs/migration.md", name)
		}
	}
	if o.templateProvider != "off" {
		if o.templateProvider != "openai" && o.templateProvider != "anthropic" {
			return errors.New("invalid template provider")
		}
		if o.grouping != "semantic" && o.grouping != "learned" && !(o.grouping == "drain" && o.masking == "classic") {
			return errors.New("template learning requires semantic or classic drain grouping")
		}
		if !c.Flags().Changed("template-model") || !c.Flags().Changed("template-input-price") || !c.Flags().Changed("template-output-price") {
			return errors.New("template provider requires explicit model and prices")
		}
		if err := o.templateConfig().Validate(); err != nil {
			return err
		}
		if o.templateSamples < 2 || o.templateSamples > 16 || o.templateCapacity < 1 || o.templateCapacity > 256 || o.templateTTL < 1 || o.templateTTL > 3600 || !(o.templateConfidence >= 0 && o.templateConfidence <= 1) {
			return errors.New("invalid template learning bounds")
		}
	}
	if o.maxEvents < 0 || (o.maxEvents == 0 && c.Name() != "controller") || o.maxEvents > 10000000 || o.queue < 1 || o.queue > 65536 || o.retain < 1 || o.retain > 100000 || o.maxBatches < 1 || o.maxBatches > 100000 || o.k.MaxBytes < 1 || o.k.MaxBytes > 1<<30 {
		return errors.New("invalid collection or queue bounds")
	}
	if o.inspectPort < 0 || o.inspectPort > 65535 {
		return errors.New("invalid inspection port")
	}
	if o.budgeted && !o.adaptive {
		return errors.New("budgeted scheduling requires --drain-adaptive")
	}
	if len(o.reviewIDs) > 0 && !o.adaptive {
		return errors.New("review veto requires adaptive scheduling")
	}
	for _, v := range []float64{o.maxCost, o.inputPrice, o.outputPrice} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return errors.New("invalid price or budget")
		}
	}
	if o.risk != "typesafe" && !o.offline && (!c.Flags().Changed("model") || !c.Flags().Changed("input-price") || !c.Flags().Changed("output-price")) {
		return errors.New("selected provider requires explicit model and prices")
	}
	if err := o.providerConfig().Validate(); err != nil {
		return err
	}
	if err := o.analysisConfig().Validate(); err != nil {
		return err
	}
	if cluster {
		raw, _ := c.Flags().GetString("since")
		n, err := seconds(raw)
		if err != nil {
			return err
		}
		o.k.Since = n
		duration, _ := c.Flags().GetInt64("duration")
		if duration < 0 || duration > 604800 {
			return errors.New("invalid duration")
		}
		o.k.Duration = time.Duration(duration) * time.Second
		if err := o.k.Validate(); err != nil {
			return err
		}
	}
	if strings.IndexByte(o.output, 0) >= 0 {
		return errors.New("invalid output path")
	}
	return nil
}
func (o *options) providerConfig() provider.Config {
	return provider.Config{Kind: o.risk, Model: o.model, InputPrice: o.inputPrice, OutputPrice: o.outputPrice, MaxCost: o.maxCost, MaxAttempts: min(o.maxAttempts, o.maxBatches*3), MaxBatches: o.maxBatches}
}
func (o *options) analysisConfig() analysis.Config {
	return analysis.Config{Offline: o.offline, Strategy: o.grouping, Masking: o.masking, Capacity: o.capacity, BatchSize: o.batch, TTL: time.Duration(o.ttl) * time.Second, Adaptive: o.adaptive, NormalInterval: uint64(o.normalInterval), ReviewIDs: o.reviewIDs, TemplateMinSamples: o.templateSamples, TemplateCapacity: o.templateCapacity, TemplateTTL: time.Duration(o.templateTTL) * time.Second, TemplateConfidence: o.templateConfidence}
}

func (o *options) templateConfig() provider.Config {
	return provider.Config{Kind: o.templateProvider, Model: o.templateModel, InputPrice: o.templateInput, OutputPrice: o.templateOutput, MaxCost: o.templateCost, MaxAttempts: o.templateRequests}
}
