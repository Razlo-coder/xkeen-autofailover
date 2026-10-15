package xkeen

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeasureDownloadRequiresCompleteSuccessfulResponse(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status, size int
		wantOK       bool
	}{
		{"complete", 200, SpeedTestBytes, true},
		{"truncated", 200, SpeedTestBytes - 1, false},
		{"server-error", 503, SpeedTestBytes, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.CopyN(w, strings.NewReader(strings.Repeat("x", tc.size)), int64(tc.size))
			}))
			defer server.Close()
			mbps, err := measureDownload(context.Background(), server.Client(), server.URL)
			if (err == nil) != tc.wantOK {
				t.Fatalf("speed=%f error=%v", mbps, err)
			}
			if tc.wantOK && mbps <= 0 {
				t.Fatalf("non-positive speed: %f", mbps)
			}
		})
	}
}
