package kube

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

func TestOIDCRefreshAuthMethods(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, want string
		reject, calls        int
		fail                 bool
	}{
		{"post", `,"token_endpoint_auth_methods_supported":["client_secret_post"]`, "post", 400, 1, false},
		{"basic", `,"token_endpoint_auth_methods_supported":["client_secret_basic"]`, "basic", 0, 1, false},
		{"both", `,"token_endpoint_auth_methods_supported":["client_secret_post","client_secret_basic"]`, "basic", 0, 1, false},
		{"omitted-basic", "", "basic", 0, 1, false},
		{"omitted-401", "", "post", 401, 2, false},
		{"omitted-400-auth", "", "post", 400, 2, false},
		{"unsupported", `,"token_endpoint_auth_methods_supported":["private_key_jwt"]`, "", 0, 0, true},
		{"explicit-basic-rejected", `,"token_endpoint_auth_methods_supported":["client_secret_basic"]`, "post", 401, 1, true},
		{"network-no-replay", "", "post", -1, 1, true},
		{"invalid-grant-no-replay", "", "post", 400, 1, true},
		{"server-error-no-replay", "", "post", 500, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := oidcFixture(t)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			config, err := clientcmd.Load(raw)
			if err != nil {
				t.Fatal(err)
			}
			c := config.AuthInfos["fixture"].AuthProvider.Config
			c["client-id"], c["client-secret"] = "synthetic + id", "synthetic & secret"
			raw, err = clientcmd.Write(*config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			token := oidcJWT(time.Now().Add(time.Hour))
			oidcWireServer(t, func(r *http.Request) (int, string) {
				if r.Method == "GET" {
					return 200, `{"token_endpoint":"http://issuer.fixture.invalid/token"` + tc.metadata + `}`
				}
				calls.Add(1)
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("refresh_token") != "synthetic-old" {
					t.Error("refresh credential missing")
				}
				id, secret, basic := r.BasicAuth()
				if basic {
					if id != url.QueryEscape(c["client-id"]) || secret != url.QueryEscape(c["client-secret"]) || r.Form.Has("client_id") || r.Form.Has("client_secret") {
						t.Error("incorrect Basic credential encoding")
					}
					if tc.want != "basic" {
						if tc.reject == -1 {
							return 0, ""
						}
						if tc.name == "invalid-grant-no-replay" {
							return tc.reject, `{"error":"invalid_grant"}`
						}
						return tc.reject, `{"error":"invalid_client"}`
					}
				} else if tc.want != "post" || r.Header.Get("Authorization") != "" || r.Form.Get("client_id") != c["client-id"] || r.Form.Get("client_secret") != c["client-secret"] {
					t.Error("incorrect body credentials")
				}
				return 200, fmt.Sprintf(`{"id_token":%q,"refresh_token":"synthetic-new"}`, token)
			}, nil)
			loaded, err := LoadContext(context.Background(), cfg())
			if err != nil {
				t.Fatal(err)
			}
			err = oidcDo(t, loaded, context.Background())
			if (err != nil) != tc.fail {
				t.Errorf("refresh error=%v, want failure=%v", err, tc.fail)
			}
			if int(calls.Load()) != tc.calls {
				t.Errorf("token requests=%d, want %d", calls.Load(), tc.calls)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.fail {
				if string(saved) != string(raw) {
					t.Error("failed refresh changed credentials")
				}
			} else {
				parsed, err := clientcmd.Load(saved)
				if err != nil {
					t.Fatal(err)
				}
				if got := parsed.AuthInfos["fixture"].AuthProvider.Config; got["id-token"] != token || got["refresh-token"] != "synthetic-new" {
					t.Error("rotation not persisted")
				}
			}
		})
	}
}
