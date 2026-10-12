package kube

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func TestDiscoveryRejectsCredentialRedirects(t *testing.T) {
	for _, auth := range []string{"bearer", "wrapped"} {
		for _, target := range []string{"https://elsewhere.invalid/pods", "https://cluster.invalid/other"} {
			t.Run(auth+"/"+target, func(t *testing.T) {
				calls := 0
				cfg := &rest.Config{Host: "https://cluster.invalid", Transport: fileRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get("Authorization") != "Bearer synthetic" {
						t.Error("missing fixture credential")
					}
					if calls > 1 {
						t.Errorf("credentials followed redirect to %s", r.URL)
					}
					status, headers, body := http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"kind":"PodList","apiVersion":"v1","items":[]}`
					if calls == 1 {
						status = http.StatusFound
						headers.Set("Location", target)
						body = ""
					}
					return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})}
				if auth == "bearer" {
					cfg.BearerToken = "synthetic"
				} else {
					// Like OIDC/exec auth, a wrapper can attach credentials even when Go's
					// redirect handling strips Authorization from the new request.
					cfg.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
						return fileRoundTripFunc(func(r *http.Request) (*http.Response, error) {
							clone := r.Clone(r.Context())
							clone.Header.Set("Authorization", "Bearer synthetic")
							return base.RoundTrip(clone)
						})
					}
				}
				client, err := HTTPClientFor(cfg)
				if err != nil {
					t.Fatal(err)
				}
				api, err := core.NewForConfigAndClient(cfg, client)
				if err != nil {
					t.Fatal(err)
				}
				_, err = SelectPod(context.Background(), api, Config{Namespace: "demo"})
				if err == nil || calls != 1 {
					t.Fatalf("redirect not rejected at client boundary: calls=%d err=%v", calls, err)
				}
			})
		}
	}
}
