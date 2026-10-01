package xkeen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeLatencyDoesNotIncludeAnotherDestinationTimeout(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	client := target.Client()
	client.Timeout = 400 * time.Millisecond
	result := probeHTTPS(context.Background(), client, []string{target.URL + "/generate_204", target.URL + "/slow"})
	if !result.OK || result.Successes != 1 || result.Total != 2 || result.Latency < 20 || result.Latency >= 300 {
		t.Fatalf("latency contains a failed destination's timeout: %+v", result)
	}
}
