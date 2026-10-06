package cli

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"github.com/sunil-sadasivan/jevernetes/internal/report"
)

var errCap = errors.New("global event cap reached")

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open configuration file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("invalid configuration file type or size")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("configuration file exceeds limit")
	}
	return raw, nil
}
func (o *options) run(cmd *cobra.Command, paths []string, stdin io.Reader) error {
	process, cancelProcess := context.WithCancel(cmd.Context())
	defer cancelProcess()
	ctx, cancel := context.WithCancel(process)
	defer cancel()
	if o.k.Duration > 0 && paths == nil {
		var bounded context.CancelFunc
		ctx, bounded = context.WithTimeout(ctx, o.k.Duration)
		defer bounded()
	}
	cfg := o.analysisConfig()
	if o.rules != "" {
		raw, err := readBounded(o.rules, 1<<20)
		if err != nil {
			return err
		}
		rules, err := analysis.LoadRules(raw, time.Now())
		if err != nil {
			return err
		}
		cfg.Rules = rules
	}
	policy := controller.DefaultPolicy()
	if o.policy != "" {
		raw, err := readBounded(o.policy, 65536)
		if err != nil {
			return err
		}
		if provider.Strict(raw, &policy) != nil {
			return errors.New("invalid controller policy")
		}
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	var store *controller.Store
	var err error
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	var sink controller.Sink
	if o.state != "" {
		if o.json && o.sink == "stdout" {
			return errors.New("controller --json conflicts with stdout notifications")
		}
		if o.sink != "stdout" && o.sink != "webhook" {
			return errors.New("invalid notification sink")
		}
		store, err = controller.Open(o.state)
		if err != nil {
			return err
		}
		defer store.Close()
		if o.sink == "stdout" {
			sink = controller.StdoutSink{Writer: cmd.OutOrStdout()}
		} else {
			raw := os.Getenv("JEV_WEBHOOK_URL")
			if raw == "" {
				path := os.Getenv("JEV_WEBHOOK_URL_FILE")
				if path != "" {
					b, e := readBounded(path, 4096)
					if e != nil {
						return errors.New("webhook configuration unavailable")
					}
					raw = strings.TrimSpace(string(b))
				}
			}
			sink, err = controller.NewWebhook(raw)
			if err != nil {
				return err
			}
		}
		health, err := net.Listen("tcp", o.listen)
		if err != nil {
			return errors.New("health listener unavailable")
		}
		listeners = append(listeners, health)
		if o.inspectPort != 0 {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", o.inspectPort))
			if err != nil {
				return errors.New("inspection listener unavailable")
			}
			listeners = append(listeners, l)
		}
	}
	var client *provider.Client
	var judge provider.Judge
	if !o.offline {
		key, err := provider.Credential(o.risk, os.Getenv)
		if err != nil {
			return err
		}
		client, err = provider.New(o.providerConfig(), key)
		if err != nil {
			return err
		}
		judge = client
	}
	var teacher *provider.Client
	if !o.offline && o.templateProvider != "off" {
		key, err := provider.Credential(o.templateProvider, os.Getenv)
		if err != nil {
			return err
		}
		teacher, err = provider.New(o.templateConfig(), key)
		if err != nil {
			return err
		}
		cfg.Teacher = teacher
	}
	contract := event.Hash([]any{"go-v1", o.risk, o.model, o.grouping, o.masking, policy})
	if store != nil && judge != nil {
		judge = controller.CachedJudge{Store: store, Next: judge, Contract: contract, TTL: cfg.TTL, Rescore: o.rescore}
	}
	engine, err := analysis.New(cfg, judge)
	if err != nil {
		return err
	}
	r := report.New(o.retain)
	state := inspect.NewState()
	if store != nil {
		state.Merge(store.Metrics())
	}
	gap := func(source event.Source, reason string) {
		r.Gap(source, reason)
		state.Add("coverage_gaps", 1)
		state.Add("coverage_"+reason, 1)
	}
	in := make(chan event.Event, o.queue)
	var total atomic.Int64
	emit := func(e event.Event) error {
		n := total.Add(1)
		if o.maxEvents > 0 && n > int64(o.maxEvents) {
			gap(e.Source, "event_limit")
			cancel()
			return errCap
		}
		state.Add("received", 1)
		if o.k.Follow {
			select {
			case in <- e:
			case <-ctx.Done():
				return ctx.Err()
			default:
				state.Add("dropped", 1)
				gap(e.Source, "queue_drop")
			}
			return nil
		}
		select {
		case in <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var collector *kube.Collector
	if paths == nil {
		kcfg, err := kube.Load(o.k)
		if err != nil {
			return err
		}
		collector, err = kube.New(kcfg, o.k)
		if err != nil {
			return err
		}
		gap(event.Source{"type": "kubernetes"}, "bounded_history")
	}
	background := make(chan error, 3)
	var workers sync.WaitGroup
	start := func(fn func() error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := fn(); err != nil {
				select {
				case background <- err:
				default:
				}
				cancelProcess()
			}
		}()
	}
	if store != nil {
		start(func() error { return store.Deliver(process, sink) })
		start(func() error { return inspect.Serve(process, listeners[0], inspect.Health(state), false) })
		if len(listeners) > 1 {
			start(func() error { return inspect.Serve(process, listeners[1], inspect.Handler(store, state), true) })
		}
	}
	collected := make(chan error, 1)
	go func() {
		defer close(in)
		if paths != nil {
			collected <- collectFiles(ctx, paths, stdin, o.k.MaxBytes, emit, gap)
			return
		}
		if o.k.Follow {
			collected <- collector.Follow(ctx, emit, gap)
		} else {
			collected <- collector.Snapshot(ctx, emit, gap)
		}
	}()
	state.Ready.Store(true)
	budgetStopped := false
	analysisErr := engine.Run(ctx, in, func(e event.Event) error {
		state.Merge(engine.Metrics)
		if store != nil {
			applyCtx := process
			var stop context.CancelFunc
			if process.Err() != nil {
				applyCtx, stop = context.WithTimeout(context.Background(), 5*time.Second)
			}
			applied, err := waitForState(ctx, func() (controller.Applied, error) {
				return store.Apply(applyCtx, e, contract, policy, time.Now().Unix())
			}, func() {
				state.Add("outbox_backpressure", 1)
				gap(e.Source, "outbox_backpressure")
			})
			state.Merge(store.Metrics())
			if stop != nil {
				stop()
			}
			if err != nil {
				cancel()
				return err
			}
			if applied.Enqueued {
				state.Add("notifications_enqueued", 1)
			}
		}
		r.Add(e)
		if e.Truncated {
			state.Add("truncated_events", 1)
		}
		if e.ParseUncertain {
			state.Add("parse_uncertain", 1)
		}
		if client != nil && paths == nil && o.k.Follow && !budgetStopped {
			usage := client.Usage()
			if usage.Breaker != "" || usage.Batches >= o.maxBatches || usage.Attempts >= o.providerConfig().MaxAttempts {
				budgetStopped = true
				gap(event.Source{"type": "kubernetes"}, "provider_budget_stopped")
				state.Add("budget_stopped", 1)
				cancel()
			}
		}
		return nil
	})
	cancel()
	collectionErr := <-collected
	if collectionErr != nil || analysisErr != nil {
		gap(event.Source{"type": "collection"}, "collection_interrupted")
	}
	state.Ready.Store(false)
	state.Stopped.Store(true)
	if store != nil && (o.hold || o.inspectPort > 0) && (analysisErr == nil || errors.Is(analysisErr, context.Canceled) || errors.Is(analysisErr, context.DeadlineExceeded)) && (collectionErr == nil || errors.Is(collectionErr, context.DeadlineExceeded) || errors.Is(collectionErr, context.Canceled) || errors.Is(collectionErr, errCap)) {
		select {
		case <-process.Done():
		case err := <-background:
			analysisErr = err
		}
	}
	cancelProcess()
	workers.Wait()
	select {
	case err := <-background:
		if analysisErr == nil {
			analysisErr = err
		}
	default:
	}
	usage := provider.Usage{Complete: true}
	if client != nil {
		usage = client.Usage()
	}
	metrics := state.Metrics()
	for k, v := range engine.Metrics {
		metrics[k] = v
	}
	mode := o.risk
	if o.offline {
		mode = "offline-rules"
	}
	value := r.Snapshot(mode, map[string]any{"namespace": o.k.Namespace, "selector": o.k.Selector}, o.k.Follow || paths == nil, usage, metrics, engine.Checkpoint())
	if teacher != nil {
		tu := teacher.Usage()
		value["provider_usage"].(map[string]any)["template"] = tu
		summary := value["summary"].(map[string]any)
		summary["total_api_requests"] = usage.Attempts + tu.Attempts
		summary["total_estimated_cost_usd"] = usage.Cost + tu.Cost
		if o.grouping == "semantic" {
			value["template_learning"] = engine.LearningReport()
		} else {
			value["template_refinement"] = engine.LearningReport()
		}
	}
	if o.output != "" {
		if err := report.Write(o.output, value); err != nil {
			return err
		}
	}
	if o.json {
		if json.NewEncoder(cmd.OutOrStdout()).Encode(value) != nil {
			return errors.New("cannot write report output")
		}
	} else {
		summary := value["summary"].(map[string]any)
		fmt.Fprintf(cmd.OutOrStdout(), "events=%v important=%v uncertain=%v unknown=%v complete=%v\n", summary["events"], summary["important"], summary["uncertain"], summary["unknown"], summary["complete_within_window"])
	}
	if cmd.Context().Err() != nil {
		return cmd.Context().Err()
	}
	if analysisErr != nil && !errors.Is(analysisErr, context.Canceled) && !errors.Is(analysisErr, context.DeadlineExceeded) {
		return analysisErr
	}
	if collectionErr != nil && !errors.Is(collectionErr, context.Canceled) && !errors.Is(collectionErr, context.DeadlineExceeded) && !errors.Is(collectionErr, errCap) {
		return collectionErr
	}
	if !value["summary"].(map[string]any)["complete_within_window"].(bool) {
		return exitError{2, ""}
	}
	return nil
}
func collectFiles(ctx context.Context, paths []string, stdin io.Reader, maxBytes int64, emit func(event.Event) error, gap func(event.Source, string)) error {
	usedStdin := false
	for _, path := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		source := event.Source{"type": "file", "path": event.Console(path)}
		var r io.Reader
		var closer io.Closer
		if path == "-" {
			if usedStdin {
				gap(source, "duplicate_stdin")
				continue
			}
			usedStdin = true
			r = stdin
			if file, ok := stdin.(*os.File); ok {
				r = contextInput{ctx: ctx, file: file}
			} else if c, ok := stdin.(io.ReadCloser); ok {
				closer = c
			}
		} else {
			f, err := os.Open(path)
			if err != nil {
				gap(source, "file_unavailable")
				continue
			}
			st, err := f.Stat()
			if err != nil || !st.Mode().IsRegular() {
				f.Close()
				gap(source, "file_type_unsupported")
				continue
			}
			r = f
			closer = f
		}
		done := make(chan struct{})
		joined := make(chan struct{})
		if closer != nil {
			go func(c io.Closer) {
				defer close(joined)
				select {
				case <-ctx.Done():
					c.Close()
				case <-done:
				}
			}(closer)
		} else {
			close(joined)
		}
		if strings.HasSuffix(path, ".gz") {
			z, err := gzip.NewReader(r)
			if err != nil {
				close(done)
				<-joined
				if closer != nil {
					closer.Close()
				}
				gap(source, "gzip_invalid")
				continue
			}
			r = z
		}
		err := event.Read(ctx, r, source, maxBytes, emit)
		close(done)
		<-joined
		if closer != nil {
			closer.Close()
		}
		if err != nil {
			if errors.Is(err, errCap) || ctx.Err() != nil {
				return err
			}
			gap(source, "input_incomplete")
		}
	}
	return nil
}

// Only pending-outbox pressure is retryable. Collection deadlines must also
// interrupt the wait even while the process keeps delivery/inspection alive.
func waitForState(ctx context.Context, apply func() (controller.Applied, error), blocked func()) (controller.Applied, error) {
	result, err := apply()
	if !errors.Is(err, controller.ErrBackpressure) {
		return result, err
	}
	blocked()
	for errors.Is(err, controller.ErrBackpressure) {
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
		result, err = apply()
	}
	return result, err
}
