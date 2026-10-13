package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/sunil-sadasivan/jevernetes/internal/analysis"
	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
	"github.com/sunil-sadasivan/jevernetes/internal/investigation"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"github.com/sunil-sadasivan/jevernetes/internal/output"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"github.com/sunil-sadasivan/jevernetes/internal/report"
	"github.com/sunil-sadasivan/jevernetes/internal/safefile"
)

var errCap = errors.New("global event cap reached")

func readBounded(ctx context.Context, path string, limit int64) ([]byte, error) {
	raw, err := safefile.ReadConfig(ctx, path, limit)
	if err != nil {
		return nil, errors.New("cannot read configuration file")
	}
	return raw, nil
}

type riskProvider interface {
	provider.Judge
	Usage() provider.Usage
}
type logCollector interface {
	Follow(context.Context, func(event.Event) error, func(event.Event) error, func(event.Source, string)) error
	Snapshot(context.Context, func(event.Event) error, func(event.Source, string)) error
}

// Dependencies are internal test seams; no endpoint or command is browser/CLI supplied.
type runtimeDeps struct {
	sourceNames map[string]string
	update      func(map[string]any)
	finished    func(map[string]any)
	quiet       bool
	provider    func(provider.Config, string) (riskProvider, error)
	collector   func(kube.Config) (logCollector, error)
}

func (o *options) runWith(cmd *cobra.Command, paths []string, stdin io.Reader, deps runtimeDeps) error {
	if o.tui {
		return o.runTerminal(cmd, paths, stdin, deps)
	}
	if deps.provider == nil {
		deps.provider = func(c provider.Config, key string) (riskProvider, error) { return provider.New(c, key) }
	}
	if deps.collector == nil {
		deps.collector = func(c kube.Config) (logCollector, error) {
			cfg, resolved, err := kube.LoadResolvedContext(cmd.Context(), c)
			if err != nil {
				return nil, err
			}
			c.Context = resolved
			o.k.Context = resolved
			return kube.New(cfg, c)
		}
	}
	if o.projectedInputs {
		cmd.SetContext(safefile.WithDeploymentInputs(cmd.Context()))
	}
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
	if o.humanRules != "" {
		reviewer, err := investigation.Reviewer(process, o.humanRules)
		if err != nil {
			return err
		}
		cfg.HumanReview = reviewer
	}

	if o.rules != "" {
		raw, err := readBounded(ctx, o.rules, 1<<20)
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
		raw, err := readBounded(ctx, o.policy, 65536)
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
					b, e := readBounded(ctx, path, 4096)
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
	var client riskProvider
	var judge provider.Judge
	if !o.offline {
		key, err := provider.CredentialContext(ctx, o.risk, os.Getenv)
		if err != nil {
			return err
		}
		client, err = deps.provider(o.providerConfig(), key)
		if err != nil {
			return err
		}
		judge = client
		if o.workers > 1 {
			judge = provider.ParallelJudge{Next: client, Workers: o.workers, BatchSize: o.batch}
		}
	}
	var teacher *provider.Client
	if !o.offline && o.templateProvider != "off" {
		key, err := provider.CredentialContext(ctx, o.templateProvider, os.Getenv)
		if err != nil {
			return err
		}
		teacher, err = provider.New(o.templateConfig(), key)
		if err != nil {
			return err
		}
		cfg.Teacher = teacher
	}
	contract := event.Hash([]any{"go-v2-attributed-criteria", o.risk, o.model, o.grouping, o.masking, policy})
	if store != nil && judge != nil {
		judge = controller.CachedJudge{Store: store, Next: judge, Contract: contract, TTL: cfg.TTL, Rescore: o.rescore}
	}
	engine, err := analysis.New(cfg, judge)
	if err != nil {
		return err
	}
	scope := func() map[string]any {
		if paths != nil {
			files := append([]string{}, paths...)
			for i, path := range files {
				if name, ok := deps.sourceNames[path]; ok {
					files[i] = name
				}
			}
			return map[string]any{"files": files}
		}
		return map[string]any{"context": o.k.Context, "namespace": o.k.Namespace, "selector": o.k.Selector, "since_seconds": o.k.Since, "tail": o.k.Tail}
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
	var viewMu sync.Mutex
	pendingEvents := map[string]event.Event{}
	snapshotView := func() map[string]any {
		u := provider.Usage{Complete: true}
		if client != nil {
			u = client.Usage()
		}
		mode := o.risk
		if o.offline {
			mode = "offline-rules"
		}
		v := r.Snapshot(mode, scope(), o.k.Follow || paths == nil, u, state.Metrics(), nil)
		tail := append([]event.Event{}, v["events"].([]event.Event)...)
		viewMu.Lock()
		seen := map[string]bool{}
		for _, e := range tail {
			seen[e.ID] = true
		}
		keys := []string{}
		for k := range pendingEvents {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			e := pendingEvents[key]
			if !seen[e.ID] {
				tail = append(tail, e)
				seen[e.ID] = true
			}
		}
		viewMu.Unlock()
		if len(tail) > o.retain {
			tail = tail[len(tail)-o.retain:]
		}
		v["tail_events"] = tail
		if o.k.Follow {
			v["live"] = map[string]any{"status": "running", "message": "Collecting and analyzing retained logs", "retained_events": len(tail), "queue_depth": len(in), "dropped": state.Metrics()["dropped"], "active_streams": state.Metrics()["active_streams"], "unfollowed_streams": state.Metrics()["unfollowed_streams"], "reconnects": state.Metrics()["coverage_stream_reconnect"], "events_per_second": float64(state.Metrics()["received"]) / max(.001, v["summary"].(map[string]any)["elapsed_seconds"].(float64))}
		}
		v["input_price"] = o.inputPrice
		v["output_price"] = o.outputPrice
		return v
	}
	pendingView := func(key string, e *event.Event) {
		if deps.update == nil {
			return
		}
		viewMu.Lock()
		if e == nil {
			delete(pendingEvents, key)
		} else if _, exists := pendingEvents[key]; exists || len(pendingEvents) < o.retain {
			pendingEvents[key] = *e
		}
		viewMu.Unlock()
	}
	// Serialize admissions across live streams so the Nth successful enqueue
	// stops intake without racing or cancelling earlier admissions.
	var admission sync.Mutex
	total := 0
	admit := func(e event.Event, final bool) error {
		if name, ok := deps.sourceNames[fmt.Sprint(e.Source["path"])]; ok {
			e.Source = event.SafeSource(e.Source)
			e.Source["path"] = name
		}
		admission.Lock()
		defer admission.Unlock()
		if o.maxEvents > 0 && total >= o.maxEvents {
			return errCap
		}
		if !final {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		admitted := false
		if deps.update != nil {
			pending := e
			pending.Importance = "pending"
			pendingView(e.ID, &pending)
			defer func() {
				if !admitted {
					pendingView(e.ID, nil)
				}
			}()
		}
		state.Add("received", 1)
		if o.k.Follow {
			select {
			case in <- e:
			default:
				state.Add("dropped", 1)
				gap(e.Source, "queue_drop")
				return nil
			}
		} else {
			select {
			case in <- e:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		admitted = true
		total++
		if o.maxEvents > 0 && total == o.maxEvents {
			gap(e.Source, "event_limit")
			cancel()
			return errCap
		}
		return nil
	}
	// Finalization never waits for space or reads more input. It shares the
	// admission lock and cap with normal intake and reports bounded queue loss.
	emit := func(e event.Event) error { return admit(e, false) }
	finalize := func(e event.Event) error { return admit(e, true) }
	var collector logCollector
	if paths == nil {
		collector, err = deps.collector(o.k)
		if err != nil {
			return err
		}
		if concrete, ok := collector.(*kube.Collector); ok && deps.update != nil {
			concrete.Stats = state.Merge
			concrete.Pending = func(source event.Source, e *event.Event) { pendingView("stream:"+event.Hash(source), e) }
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
	if deps.update != nil {
		start(func() error {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				deps.update(snapshotView())
				select {
				case <-process.Done():
					return nil
				case <-ticker.C:
				}
			}
		})
	}

	if store != nil {
		start(func() error { return store.Deliver(process, sink) })
		start(func() error { return inspect.Serve(process, listeners[0], inspect.Health(state), false) })
		if len(listeners) > 1 {
			start(func() error { return inspect.Serve(process, listeners[1], inspect.Handler(store, state), true) })
		}
	}
	collected := make(chan error, 1)
	state.Ready.Store(true)
	go func() {
		defer close(in)
		defer state.Ready.Store(false)
		if paths != nil {
			collected <- collectFiles(ctx, paths, stdin, o.k.MaxBytes, emit, gap)
			return
		}
		if o.k.Follow {
			collected <- collector.Follow(ctx, emit, finalize, gap)
		} else {
			collected <- collector.Snapshot(ctx, emit, gap)
		}
	}()
	budgetStopped := false
	var evidenceErr error
	// Intake limits stop producers only. Admitted provider work uses the process
	// context and drains; operator cancellation still interrupts it.
	analysisErr := engine.Run(process, in, func(e event.Event) error {
		state.Merge(engine.Metrics)
		if store != nil {
			// Admission commits us to bounded persistence even if the operator
			// cancels during the transaction. Detach before Apply, never after
			// checking Err (which races cancellation); provider work stays on
			// process and backpressure waits stay on the intake context.
			applied, err := waitForState(ctx, func() (controller.Applied, error) {
				applyCtx, stop := context.WithTimeout(context.WithoutCancel(process), 5*time.Second)
				defer stop()
				return store.Apply(applyCtx, e, contract, policy, time.Now().Unix())
			}, func() {
				state.Add("outbox_backpressure", 1)
				gap(e.Source, "outbox_backpressure")
			})
			state.Merge(store.Metrics())
			if err != nil {
				cancel()
				return err
			}
			if applied.Enqueued {
				state.Add("notifications_enqueued", 1)
			}
		}
		r.Add(e)
		if deps.update != nil {
			pendingView(e.ID, nil)
		}
		if !o.json && !deps.quiet && evidenceErr == nil && process.Err() == nil {
			writer := cmd.OutOrStdout()
			if store != nil {
				writer = cmd.ErrOrStderr()
			}
			var evidence bytes.Buffer
			source, _ := json.Marshal(e.Source)
			fmt.Fprintf(&evidence, "[%s/%s/%s] %s #%s\n", e.Importance, e.Severity, e.Category, event.Console(string(source)), e.ID)
			for _, line := range strings.Split(e.Text, "\n") {
				fmt.Fprintf(&evidence, "  %s\n", event.Console(line))
			}
			if e.AnalysisError != "" {
				fmt.Fprintf(&evidence, "  analysis: %s\n", event.Console(e.AnalysisError))
			}
			writeCtx, stop := context.WithTimeout(process, time.Second)
			_, evidenceErr = output.Write(writeCtx, writer, evidence.Bytes())
			stop()
			if evidenceErr != nil {
				// Stop fresh intake and suppress further evidence writes, but
				// persist/report every admitted event before surfacing the error.
				cancel()
			}
		}
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
	if store != nil && o.hold && !budgetStopped && evidenceErr == nil && (analysisErr == nil || errors.Is(analysisErr, context.Canceled) || errors.Is(analysisErr, context.DeadlineExceeded)) && (collectionErr == nil || errors.Is(collectionErr, context.DeadlineExceeded) || errors.Is(collectionErr, context.Canceled) || errors.Is(collectionErr, errCap)) {
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
	value := r.Snapshot(mode, scope(), o.k.Follow || paths == nil, usage, metrics, engine.Checkpoint())
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
	value["input_price"] = o.inputPrice
	value["output_price"] = o.outputPrice
	if o.k.Follow {
		value["tail_events"] = value["events"]
		value["live"] = map[string]any{"status": "stopped", "message": "Collection stopped; retained evidence remains available", "active_streams": 0, "queue_depth": 0, "retained_events": len(value["events"].([]event.Event)), "events_per_second": float64(metrics["received"]) / max(.001, value["summary"].(map[string]any)["elapsed_seconds"].(float64)), "dropped": metrics["dropped"], "unfollowed_streams": metrics["unfollowed_streams"], "reconnects": metrics["coverage_stream_reconnect"]}
	}
	if deps.finished != nil {
		deps.finished(value)
	}
	if o.output != "" {
		if err := report.Write(o.output, value); err != nil {
			return err
		}
	}
	if o.json {
		if output.Final(cmd.OutOrStdout(), func(w io.Writer) error { return json.NewEncoder(w).Encode(value) }) != nil {
			return errors.New("cannot write report output")
		}
	} else if evidenceErr == nil && !deps.quiet {
		summary := value["summary"].(map[string]any)
		writer := cmd.OutOrStdout()
		if store != nil {
			writer = cmd.ErrOrStderr()
		}
		if err := output.Final(writer, func(w io.Writer) error {
			_, err := fmt.Fprintf(w, "events=%v important=%v uncertain=%v unknown=%v complete=%v\n", summary["events"], summary["important"], summary["uncertain"], summary["unknown"], summary["complete_within_window"])
			if err == nil && o.top > 0 {
				raw, _ := json.Marshal(value["important_groups"])
				var groups []struct {
					Text  string `json:"text"`
					Count int    `json:"count"`
				}
				_ = json.Unmarshal(raw, &groups)
				for _, g := range groups[:min(o.top, len(groups))] {
					if _, err = fmt.Fprintf(w, "×%d %s\n", g.Count, event.Console(g.Text)); err != nil {
						break
					}
				}
			}
			return err
		}); err != nil {
			return err
		}
	}
	if budgetStopped {
		return errors.New("provider budget exhausted; collection stopped, restart under supervision after reviewing budget policy")
	}
	if cmd.Context().Err() != nil {
		return cmd.Context().Err()
	}
	if evidenceErr != nil {
		return fmt.Errorf("cannot write evidence output: %w", evidenceErr)
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
			f, err := safefile.Open(ctx, path)
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
