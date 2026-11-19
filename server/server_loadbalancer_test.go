package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traefik/traefik/metrics"
	"github.com/traefik/traefik/types"
)

func TestConfigureBackends(t *testing.T) {
	validMethod := "Drr"
	defaultMethod := "wrr"

	testCases := []struct {
		desc               string
		lb                 *types.LoadBalancer
		expectedMethod     string
		expectedStickiness *types.Stickiness
	}{
		{
			desc: "valid load balancer method with sticky enabled",
			lb: &types.LoadBalancer{
				Method:     validMethod,
				Stickiness: &types.Stickiness{},
			},
			expectedMethod:     validMethod,
			expectedStickiness: &types.Stickiness{},
		},
		{
			desc: "valid load balancer method with sticky disabled",
			lb: &types.LoadBalancer{
				Method:     validMethod,
				Stickiness: nil,
			},
			expectedMethod: validMethod,
		},
		{
			desc: "invalid load balancer method with sticky enabled",
			lb: &types.LoadBalancer{
				Method:     "Invalid",
				Stickiness: &types.Stickiness{},
			},
			expectedMethod:     defaultMethod,
			expectedStickiness: &types.Stickiness{},
		},
		{
			desc: "invalid load balancer method with sticky disabled",
			lb: &types.LoadBalancer{
				Method:     "Invalid",
				Stickiness: nil,
			},
			expectedMethod: defaultMethod,
		},
		{
			desc:           "missing load balancer",
			lb:             nil,
			expectedMethod: defaultMethod,
		},
	}

	for _, test := range testCases {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()
			backend := &types.Backend{
				LoadBalancer: test.lb,
			}

			configureBackends(map[string]*types.Backend{
				"backend": backend,
			})

			expected := types.LoadBalancer{
				Method:     test.expectedMethod,
				Stickiness: test.expectedStickiness,
			}

			assert.Equal(t, expected, *backend.LoadBalancer)
		})
	}
}

func TestStickyCookieFormats(t *testing.T) {
	for _, test := range []struct {
		backend string
		value   string
	}{
		{"http://app:8080/api", "app:8080/api"},
		{"http://[::1]:8080", "[::1]:8080"},
		{"https://app:8443/api", "https://app:8443/api"},
	} {
		t.Run(test.backend, func(t *testing.T) {
			server, err := url.Parse(test.backend)
			require.NoError(t, err)
			sticky := newStickySession("affinity", &types.Stickiness{})
			recorder := httptest.NewRecorder()
			var writer http.ResponseWriter = recorder
			sticky.StickBackend(server, &writer)
			cookies := recorder.Result().Cookies()
			require.Len(t, cookies, 1)
			require.Equal(t, test.value, cookies[0].Value)
			require.Equal(t, 3600, cookies[0].MaxAge)

			for _, value := range []string{cookies[0].Value, test.backend} {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(&http.Cookie{Name: "affinity", Value: value})
				got, found, err := sticky.GetBackend(req, []*url.URL{server})
				require.NoError(t, err)
				require.True(t, found)
				assert.Equal(t, server, got)
			}
		})
	}
}

func TestStickyCookieLifetime(t *testing.T) {
	for _, method := range []string{"wrr", "drr"} {
		t.Run(method, func(t *testing.T) {
			backend := &types.Backend{
				LoadBalancer: &types.LoadBalancer{
					Method:     method,
					Stickiness: &types.Stickiness{CookieName: "affinity", MaxAge: 1800},
				},
				Servers: map[string]types.Server{
					"a": {URL: "http://a:8080", Weight: 1},
					"b": {URL: "http://b:8080", Weight: 1},
				},
			}
			s := &Server{metricsRegistry: metrics.NewVoidRegistry()}
			lb, err := s.buildLoadBalancer("frontend", "checkout", backend, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				fmt.Fprint(w, req.URL.Host)
			}))
			require.NoError(t, err)

			for _, test := range []struct {
				cookieValue string
				issueCookie bool
			}{
				{"", true},
				{"retired:8080", true},
				{"b:8080", false},
				{"http://b:8080", false},
			} {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				if test.cookieValue != "" {
					req.AddCookie(&http.Cookie{Name: "affinity", Value: test.cookieValue})
				}
				recorder := httptest.NewRecorder()
				lb.ServeHTTP(recorder, req)

				if test.issueCookie {
					cookies := recorder.Result().Cookies()
					require.Len(t, cookies, 1)
					assert.Equal(t, "affinity", cookies[0].Name)
					assert.Equal(t, recorder.Body.String(), cookies[0].Value)
					assert.Equal(t, 1800, cookies[0].MaxAge)
				} else {
					assert.Equal(t, "b:8080", recorder.Body.String())
					assert.Empty(t, recorder.Header().Values("Set-Cookie"))
				}
			}
		})
	}
}
