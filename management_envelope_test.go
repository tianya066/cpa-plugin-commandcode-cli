package plugin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// The host decodes the management.handle envelope result into
// pluginapi.ManagementResponse, whose fields carry no JSON tags. Any wire form
// that only differs by underscore (status_code vs StatusCode) is silently
// ignored by encoding/json, which turns every 4xx/5xx into HTTP 200 and breaks
// the panel's 401/409 handling. Decode through the SDK type here, not through a
// local mirror, so this test fails if the encoding drifts.
func TestManagementEnvelopeRoundTripsThroughSDKType(t *testing.T) {
	for _, status := range []int{0, http.StatusOK, http.StatusBadRequest, http.StatusConflict, http.StatusUnauthorized} {
		raw, err := ManagementEnvelope(pluginapi.ManagementResponse{
			StatusCode: status,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"error":"x"}`),
		})
		if err != nil {
			t.Fatalf("status %d: encode: %v", status, err)
		}
		var envelope pluginabi.Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("status %d: envelope: %v", status, err)
		}
		if !envelope.OK {
			t.Fatalf("status %d: envelope not ok: %s", status, raw)
		}
		var decoded pluginapi.ManagementResponse
		if err := json.Unmarshal(envelope.Result, &decoded); err != nil {
			t.Fatalf("status %d: result: %v", status, err)
		}
		want := status
		if want == 0 {
			want = http.StatusOK
		}
		if decoded.StatusCode != want {
			t.Fatalf("status %d round-tripped as %d; the host would answer HTTP %d", status, decoded.StatusCode, decoded.StatusCode)
		}
		if string(decoded.Body) != `{"error":"x"}` {
			t.Fatalf("status %d: body round-tripped as %q", status, decoded.Body)
		}
		if decoded.Headers.Get("Content-Type") != "application/json" {
			t.Fatalf("status %d: headers round-tripped as %v", status, decoded.Headers)
		}
	}
}
