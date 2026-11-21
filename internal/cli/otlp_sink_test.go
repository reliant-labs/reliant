package cli

import (
	"compress/gzip"
	"io"
	"net/http"
	"sync"

	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpSink is a fake OTLP/HTTP collector shared by the telemetry tests (the
// untagged local-agent tests and the e2e trace-depth test): it keeps every
// span it is sent, and the metric names of every metrics export by
// service.name. Logs and profiles are accepted and ignored. The zero value is
// ready to use. Its trace accessor is in trace_depth_e2e_test.go, the one
// file that reads spans, so the untagged build declares no unused method.
type otlpSink struct {
	mu      sync.Mutex
	spans   []*tracepb.Span
	metrics map[string]map[string]bool // service.name -> metric names
}

func (s *otlpSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/traces" && r.URL.Path != "/v1/metrics" {
		w.WriteHeader(http.StatusOK)
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = gz
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var out []byte
	if r.URL.Path == "/v1/traces" {
		out, err = s.recordTraces(raw)
	} else {
		out, err = s.recordMetrics(raw)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}

func (s *otlpSink) recordTraces(raw []byte) ([]byte, error) {
	var req collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	s.mu.Lock()
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			s.spans = append(s.spans, ss.GetSpans()...)
		}
	}
	s.mu.Unlock()
	return proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
}

func (s *otlpSink) recordMetrics(raw []byte) ([]byte, error) {
	req := pmetricotlp.NewExportRequest()
	if err := req.UnmarshalProto(raw); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.metrics == nil {
		s.metrics = map[string]map[string]bool{}
	}
	rms := req.Metrics().ResourceMetrics()
	for i := 0; i < rms.Len(); i++ {
		svc, _ := rms.At(i).Resource().Attributes().Get("service.name")
		names := s.metrics[svc.AsString()]
		if names == nil {
			names = map[string]bool{}
			s.metrics[svc.AsString()] = names
		}
		sms := rms.At(i).ScopeMetrics()
		for j := 0; j < sms.Len(); j++ {
			ms := sms.At(j).Metrics()
			for k := 0; k < ms.Len(); k++ {
				names[ms.At(k).Name()] = true
			}
		}
	}
	s.mu.Unlock()
	return pmetricotlp.NewExportResponse().MarshalProto()
}

// names returns the metric names service svc exported.
func (s *otlpSink) names(svc string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.metrics[svc] {
		out = append(out, n)
	}
	return out
}
