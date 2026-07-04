// Package httpx builds a single, well-tuned *http.Client to be shared across the
// whole process. The defaults in net/http (notably MaxIdleConnsPerHost=2)
// serialize concurrent requests against one host (our SearXNG instance), which
// would throttle the parallel fan-out; we raise the relevant knobs here.
package httpx

import (
	"net"
	"net/http"
	"time"
)

// New returns a tuned client. The overall per-request deadline is applied via
// context (context.WithTimeout) at the call site, not here, so callers stay in
// control of cancellation.
func New() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   50, // default is 2 — the key fix for fan-out
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Transport: transport}
}
