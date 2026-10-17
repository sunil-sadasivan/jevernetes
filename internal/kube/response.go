package kube

import (
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

// Configure once at package initialization, before any client-go workers start.
// Dependency diagnostics can contain response bodies, URLs and credentials, and
// klog's default synchronous stderr writer bypasses our cancellable output owner.
// Application boundaries report fixed errors; dependency logs are discarded
// without an output goroutine, queue, or descriptor.
func init() { klog.SetLoggerWithOptions(logr.Discard(), klog.ContextualLogger(true)) }

const maxMetadataBytes = 8 << 20
const maxAPIErrorBytes = 64 << 10

var podLogPath = regexp.MustCompile(`/api/v1/namespaces/[^/]+/pods/[^/]+/log$`)
var errAPIResponseTooLarge = errors.New("Kubernetes response too large")

type responseTransport struct{ base http.RoundTripper }

func (t responseTransport) WrappedRoundTripper() http.RoundTripper { return t.base }
func (t responseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil || res.Body == nil {
		return res, err
	}
	limit := int64(maxMetadataBytes)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		limit = maxAPIErrorBytes
	} else if req.Method == http.MethodGet && podLogPath.MatchString(req.URL.EscapedPath()) {
		return res, nil
	}
	res.Body = &apiResponseBody{ReadCloser: res.Body, remaining: limit}
	return res, nil
}

// Count decoded HTTP bytes (including transparently decompressed bodies) before
// client-go's ReadAll/JSON/protobuf decoding. The sentinel byte distinguishes an
// exact limit from truncation; truncated objects never reach the decoder.
type apiResponseBody struct {
	io.ReadCloser
	remaining int64
}

func (b *apiResponseBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, errAPIResponseTooLarge
	}
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return n, errAPIResponseTooLarge
	}
	return n, err
}
