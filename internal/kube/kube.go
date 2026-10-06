// Package kube is the sole cluster boundary: pod reads, log GETs and port-forward.
package kube

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

type Config struct {
	Context, Namespace, Selector, Pod string
	InCluster, Follow, NoPrevious     bool
	Since                             int64
	Tail                              int64
	MaxBytes                          int64
	MaxStreams                        int
	Duration                          time.Duration
}

func (c Config) Validate() error {
	if c.Namespace != "" && len(validation.IsDNS1123Label(c.Namespace)) > 0 {
		return errors.New("invalid namespace")
	}
	if c.Pod != "" && len(validation.IsDNS1123Subdomain(c.Pod)) > 0 {
		return errors.New("invalid pod")
	}
	if c.Context != "" && c.InCluster {
		return errors.New("context conflicts with in-cluster")
	}
	if len(c.Context) > 256 || strings.ContainsAny(c.Context, "\r\n\x00") {
		return errors.New("invalid context")
	}
	if len(c.Selector) > 1024 {
		return errors.New("invalid selector")
	}
	if _, err := labels.Parse(c.Selector); err != nil {
		return errors.New("invalid selector")
	}
	if c.Pod != "" && c.Selector != "" {
		return errors.New("pod conflicts with selector")
	}
	if c.Since < 0 || c.Since > 31536000 || c.Tail < 0 || c.MaxBytes < 1 || c.MaxBytes > 1<<30 || c.MaxStreams < 1 || c.MaxStreams > 1024 || c.Duration < 0 {
		return errors.New("invalid collection bounds")
	}
	return nil
}
func Load(c Config) (*rest.Config, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var cfg *rest.Config
	var err error
	if c.InCluster {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: c.Context}).ClientConfig()
	}
	if err != nil {
		return nil, errors.New("Kubernetes configuration unavailable")
	}
	cfg.Timeout = 15 * time.Second
	cfg.QPS = 5
	cfg.Burst = 10
	cfg.UserAgent = "jevernetes-go"
	return cfg, nil
}

type API interface {
	Pods(string) core.PodInterface
}
type Collector struct {
	API     API
	Config  Config
	OpenLog func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error)
}

func New(cfg *rest.Config, c Config) (*Collector, error) {
	copy := rest.CopyConfig(cfg)
	copy.Timeout = 0
	httpClient, err := rest.HTTPClientFor(copy)
	if err != nil {
		return nil, errors.New("Kubernetes transport unavailable")
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }
	client, err := core.NewForConfigAndClient(copy, httpClient)
	if err != nil {
		return nil, errors.New("Kubernetes client unavailable")
	}
	return &Collector{API: client, Config: c, OpenLog: func(ctx context.Context, ns, pod string, o *v1.PodLogOptions) (io.ReadCloser, error) {
		return client.Pods(ns).GetLogs(pod, o).Stream(ctx)
	}}, nil
}
func SelectPod(ctx context.Context, api API, c Config) (*v1.Pod, error) {
	if c.Namespace == "" {
		return nil, errors.New("namespace required")
	}
	if c.Pod != "" {
		p, err := api.Pods(c.Namespace).Get(ctx, c.Pod, metav1.GetOptions{})
		if err != nil || p.Name != c.Pod || p.Namespace != c.Namespace || p.Status.Phase != v1.PodRunning || p.DeletionTimestamp != nil {
			return nil, errors.New("exact running controller pod unavailable")
		}
		return p, nil
	}
	selector := c.Selector
	if selector == "" {
		selector = "app=jevernetes"
	}
	pods, err := api.Pods(c.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, FieldSelector: "status.phase=Running", Limit: 2})
	if err != nil || pods.Continue != "" || len(pods.Items) != 1 {
		return nil, errors.New("controller selection must resolve to exactly one running pod")
	}
	p := pods.Items[0]
	if p.Namespace != c.Namespace || p.Status.Phase != v1.PodRunning || p.DeletionTimestamp != nil {
		return nil, errors.New("controller pod unavailable")
	}
	return &p, nil
}

type stream struct {
	pod         v1.Pod
	name, kind  string
	previous    bool
	restart     int32
	containerID string
}

func streams(p v1.Pod, previous bool, live bool) []stream {
	out := []stream{}
	sets := []struct {
		kind   string
		status []v1.ContainerStatus
	}{{"container", p.Status.ContainerStatuses}, {"init", p.Status.InitContainerStatuses}, {"ephemeral", p.Status.EphemeralContainerStatuses}}
	for _, set := range sets {
		for _, s := range set.status {
			if !live || s.State.Running != nil {
				out = append(out, stream{p, s.Name, set.kind, false, s.RestartCount, s.ContainerID})
			}
			if previous && !live && s.RestartCount > 0 {
				out = append(out, stream{p, s.Name, set.kind, true, s.RestartCount - 1, previousID(s)})
			}
		}
	}
	return out
}
func (s stream) source(c Config) event.Source {
	return event.Source{"type": "kubernetes", "context": c.Context, "namespace": s.pod.Namespace, "pod": s.pod.Name, "pod_uid": string(s.pod.UID), "container": s.name, "kind": s.kind, "previous": s.previous, "restart_count": s.restart, "container_id": s.containerID}
}
func (c *Collector) list(ctx context.Context) ([]v1.Pod, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if c.Config.Pod != "" {
		p, err := c.API.Pods(c.Config.Namespace).Get(ctx, c.Config.Pod, metav1.GetOptions{})
		if err != nil {
			return nil, errors.New("pod collection failed")
		}
		return []v1.Pod{*p}, nil
	}
	out := []v1.Pod{}
	cont := ""
	for {
		list, err := c.API.Pods(c.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: c.Config.Selector, Limit: 200, Continue: cont})
		if err != nil {
			return nil, errors.New("pod discovery failed")
		}
		out = append(out, list.Items...)
		if len(out) > 4096 {
			return nil, errors.New("pod discovery capacity exceeded")
		}
		cont = list.Continue
		if cont == "" {
			return out, nil
		}
	}
}
func (c *Collector) Snapshot(ctx context.Context, emit func(event.Event) error, gap func(event.Source, string)) error {
	pods, err := c.list(ctx)
	if err != nil {
		return err
	}
	for _, p := range pods {
		for _, s := range streams(p, !c.Config.NoPrevious, false) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			source := s.source(c.Config)
			bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := c.validateStream(bounded, s); err != nil {
				gap(source, identityGap(err))
				cancel()
				continue
			}
			r, err := c.OpenLog(bounded, p.Namespace, p.Name, &v1.PodLogOptions{Container: s.name, Previous: s.previous, Timestamps: true, SinceSeconds: &c.Config.Since, TailLines: &c.Config.Tail})
			if err != nil {
				gap(source, "log_unavailable")
				cancel()
				continue
			}
			var emitErr error
			err = event.Read(bounded, r, source, c.Config.MaxBytes, func(e event.Event) error { emitErr = emit(e); return emitErr })
			r.Close()
			cancel()
			if emitErr != nil {
				return emitErr
			}
			if err != nil {
				gap(source, "log_incomplete")
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
		}
	}
	return nil
}

func previousID(s v1.ContainerStatus) string {
	if s.LastTerminationState.Terminated != nil {
		return s.LastTerminationState.Terminated.ContainerID
	}
	return ""
}

// The log API has no UID precondition. Validate immediately before every open;
// an API-server replacement after this GET remains an unavoidable race.
func (c *Collector) validateStream(ctx context.Context, want stream) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := c.API.Pods(want.pod.Namespace).Get(ctx, want.pod.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return errIdentityChanged
	}
	if err != nil {
		return errIdentityUnavailable
	}
	// An instance discovered without a runtime ID cannot ever be validated.
	if want.containerID == "" {
		return errIdentityChanged
	}
	if p.UID == "" {
		return errIdentityUnavailable
	}
	if p.UID != want.pod.UID || p.Name != want.pod.Name || p.Namespace != want.pod.Namespace || p.DeletionTimestamp != nil {
		return errIdentityChanged
	}
	for _, got := range streams(*p, want.previous, c.Config.Follow) {
		if got.name == want.name && got.kind == want.kind && got.previous == want.previous {
			if got.restart != want.restart {
				return errIdentityChanged
			}
			if got.containerID == "" {
				return errIdentityUnavailable
			}
			if got.containerID == want.containerID {
				return nil
			}
			return errIdentityChanged
		}
	}
	return errIdentityChanged
}

var errIdentityChanged = errors.New("log identity changed")
var errIdentityUnavailable = errors.New("log identity unavailable")

func identityGap(err error) string {
	if errors.Is(err, errIdentityChanged) {
		return "identity_changed"
	}
	return "identity_unavailable"
}
