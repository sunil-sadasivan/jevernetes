package kube

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/safefile"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
)

var errOIDC = errors.New("Kubernetes OIDC credentials unavailable")

const oidcTimeout = 15 * time.Second

type oidcTransport struct {
	base   http.RoundTripper
	config map[string]string
	store  *oidcStore
}

func (t *oidcTransport) WrappedRoundTripper() http.RoundTripper { return t.base }
func (t *oidcTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), oidcTimeout)
	token, err := t.token(ctx)
	cancel()
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, errOIDC
	}
	copy := req.Clone(req.Context())
	copy.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(copy)
}
func configureOIDC(cfg *rest.Config, tc *transport.Config) error {
	if cfg.AuthProvider == nil || cfg.AuthProvider.Name != "oidc" {
		return errOIDC
	}
	c := maps.Clone(cfg.AuthProvider.Config)
	if c["idp-issuer-url"] == "" || c["client-id"] == "" {
		return errOIDC
	}
	store, _ := cfg.AuthConfigPersister.(*oidcStore)
	original := tc.WrapTransport
	tc.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		var rt http.RoundTripper = &oidcTransport{base: base, config: c, store: store}
		if original != nil {
			rt = original(rt)
		}
		return rt
	}
	return nil
}
func validIDToken(token string) bool {
	if len(token) > 1<<20 || strings.ContainsAny(token, " \r\n\t") {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return false
	}
	var c struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(b, &c) != nil {
		return false
	}
	return c.Exp > float64(time.Now().Add(10*time.Second).Unix())
}
func (t *oidcTransport) token(ctx context.Context) (string, error) {
	// Cached tokens never contact the issuer or require a writable configuration.
	if validIDToken(t.config["id-token"]) {
		return t.config["id-token"], nil
	}
	if t.store == nil {
		return "", errOIDC
	}
	tx, err := t.store.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.close()
	_, c, _, err := tx.read()
	if err != nil {
		return "", err
	}
	// A change to provider identity/configuration requires explicit client reload.
	if !sameOIDCSettings(t.config, c) {
		return "", errOIDC
	}
	if validIDToken(c["id-token"]) {
		return c["id-token"], nil
	}
	if c["refresh-token"] == "" {
		return "", errOIDC
	}
	next, err := refreshOIDC(ctx, c, t.store.path)
	if err != nil {
		return "", err
	}
	if err = tx.persist(c, next); err != nil {
		return "", err
	}
	return next["id-token"], nil
}
func sameOIDCSettings(a, b map[string]string) bool {
	a = maps.Clone(a)
	b = maps.Clone(b)
	for _, k := range []string{"id-token", "refresh-token"} {
		delete(a, k)
		delete(b, k)
	}
	return maps.Equal(a, b)
}
func oidcURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
func refreshOIDC(ctx context.Context, c map[string]string, path string) (map[string]string, error) {
	issuer := c["idp-issuer-url"]
	if !oidcURL(issuer) {
		return nil, errOIDC
	}
	// Clone rather than mutate shared transport state. Honor the default proxy and
	// dial hooks, but always bound real transport phases and the entire exchange.
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errOIDC
	}
	tr := base.Clone()
	tr.DisableKeepAlives = true
	dial := tr.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return dial(bounded, network, address)
	}
	tr.TLSHandshakeTimeout = 5 * time.Second
	tr.ResponseHeaderTimeout = 5 * time.Second
	tr.ExpectContinueTimeout = time.Second
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	defer tr.CloseIdleConnections()
	var ca []byte
	var err error
	if data := c["idp-certificate-authority-data"]; data != "" {
		ca, err = base64.StdEncoding.DecodeString(data)
	} else if ref := c["idp-certificate-authority"]; ref != "" {
		if !filepath.IsAbs(ref) {
			ref = filepath.Join(filepath.Dir(path), ref)
		}
		ca, err = safefile.Read(ctx, ref, 1<<20)
	}
	if err != nil || ((c["idp-certificate-authority-data"] != "" || c["idp-certificate-authority"] != "") && len(ca) == 0) {
		return nil, errOIDC
	}
	if len(ca) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, errOIDC
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	client := &http.Client{Transport: tr, Timeout: oidcTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errOIDC }}
	request := func(method, endpoint, body string, basic bool) ([]byte, int, error) {
		req, e := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
		if e != nil {
			return nil, 0, errOIDC
		}
		if method == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if basic {
				req.SetBasicAuth(url.QueryEscape(c["client-id"]), url.QueryEscape(c["client-secret"]))
			}
		}
		res, e := client.Do(req)
		if e != nil {
			return nil, 0, errOIDC
		}
		defer res.Body.Close()
		b, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		if e != nil || len(b) > 1<<20 {
			return nil, 0, errOIDC
		}
		return b, res.StatusCode, nil
	}
	b, status, err := request("GET", strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", "", false)
	if err != nil || status != 200 {
		return nil, errOIDC
	}
	var meta struct {
		Issuer   string `json:"issuer"`
		Endpoint string `json:"token_endpoint"`
	}
	if json.Unmarshal(b, &meta) != nil || (meta.Issuer != "" && meta.Issuer != issuer) || !oidcURL(meta.Endpoint) {
		return nil, errOIDC
	}
	if strings.HasPrefix(issuer, "https:") && !strings.HasPrefix(meta.Endpoint, "https:") {
		return nil, errOIDC
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c["refresh-token"]}}
	b, status, err = request("POST", meta.Endpoint, form.Encode(), true)
	// Only explicit authentication rejection permits retry with client credentials
	// in the body. Never replay a possibly consumed refresh after an I/O failure.
	if err == nil && status == http.StatusUnauthorized {
		form.Set("client_id", c["client-id"])
		form.Set("client_secret", c["client-secret"])
		b, status, err = request("POST", meta.Endpoint, form.Encode(), false)
	}
	if err != nil || status != 200 {
		return nil, errOIDC
	}
	var token struct {
		ID      string `json:"id_token"`
		Refresh string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &token) != nil || !validIDToken(token.ID) || strings.ContainsAny(token.Refresh, "\r\n\x00") {
		return nil, errOIDC
	}
	next := maps.Clone(c)
	next["id-token"] = token.ID
	if token.Refresh != "" {
		next["refresh-token"] = token.Refresh
	}
	return next, nil
}
