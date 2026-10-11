package kube

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/sunil-sadasivan/jevernetes/internal/safefile"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
)

// Load bytes ourselves: client-go's pathname loader and TLS/token helpers use
// blocking opens. Preserve first-file-wins merging and origin-relative paths,
// without its optional migration writes or implicit in-cluster fallback.
func loadLocal(ctx context.Context, current string) (*rest.Config, error) {
	paths := clientcmd.NewDefaultClientConfigLoadingRules().GetLoadingPrecedence()
	if len(paths) > 32 {
		return nil, errors.New("too many configuration files")
	}
	merged := api.NewConfig()
	for _, path := range paths {
		if path == "" {
			continue
		}
		raw, err := safefile.Read(ctx, path, 1<<20)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		c, err := clientcmd.Load(raw)
		if err != nil {
			return nil, err
		}
		for _, v := range c.Clusters {
			v.LocationOfOrigin = path
		}
		for _, v := range c.AuthInfos {
			v.LocationOfOrigin = path
		}
		for _, v := range c.Contexts {
			v.LocationOfOrigin = path
		}
		if err := clientcmd.ResolveLocalPaths(c); err != nil {
			return nil, err
		}
		for k, v := range c.Clusters {
			if _, ok := merged.Clusters[k]; !ok {
				merged.Clusters[k] = v
			}
		}
		for k, v := range c.AuthInfos {
			if _, ok := merged.AuthInfos[k]; !ok {
				merged.AuthInfos[k] = v
			}
		}
		for k, v := range c.Contexts {
			if _, ok := merged.Contexts[k]; !ok {
				merged.Contexts[k] = v
			}
		}
		if merged.CurrentContext == "" {
			merged.CurrentContext = c.CurrentContext
		}
	}
	if current == "" {
		current = merged.CurrentContext
	}
	selected := merged.Contexts[current]
	if selected == nil {
		return nil, errors.New("missing context")
	}
	if cluster := merged.Clusters[selected.Cluster]; cluster != nil {
		if len(cluster.CertificateAuthorityData) == 0 && cluster.CertificateAuthority != "" {
			raw, err := safefile.Read(ctx, cluster.CertificateAuthority, 1<<20)
			if err != nil {
				return nil, err
			}
			cluster.CertificateAuthorityData = raw
		}
		cluster.CertificateAuthority = ""
	}
	tokenFile := ""
	if auth := merged.AuthInfos[selected.AuthInfo]; auth != nil {
		for _, ref := range []struct {
			path *string
			data *[]byte
		}{{&auth.ClientCertificate, &auth.ClientCertificateData}, {&auth.ClientKey, &auth.ClientKeyData}} {
			if len(*ref.data) == 0 && *ref.path != "" {
				raw, err := safefile.Read(ctx, *ref.path, 1<<20)
				if err != nil {
					return nil, err
				}
				*ref.data = raw
			}
			*ref.path = ""
		}
		if auth.TokenFile != "" {
			tokenFile = auth.TokenFile
			token, err := readToken(ctx, tokenFile)
			if err != nil {
				return nil, err
			}
			auth.Token = token
			auth.TokenFile = ""
		}
	}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(*merged, current, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, err
	}
	if tokenFile != "" {
		cfg.BearerToken = ""
		cfg.BearerTokenFile = ""
		cfg.Wrap(func(next http.RoundTripper) http.RoundTripper { return fileTokenTransport{next, tokenFile} })
	}
	return cfg, nil
}

func readToken(ctx context.Context, path string) (string, error) {
	raw, err := safefile.Read(ctx, path, 16384)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 {
		return "", errors.New("invalid token file")
	}
	return token, nil
}

type fileTokenTransport struct {
	next http.RoundTripper
	path string
}

func (t fileTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" {
		return t.next.RoundTrip(req)
	}
	token, err := readToken(req.Context(), t.path)
	if err != nil {
		return nil, errors.New("Kubernetes token file unavailable")
	}
	copy := req.Clone(req.Context())
	copy.Header.Set("Authorization", "Bearer "+token)
	return t.next.RoundTrip(copy)
}
