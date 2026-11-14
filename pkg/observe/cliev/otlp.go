package cliev

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strconv"

	"github.com/reliant-labs/forge/pkg/observe"
)

// The OTLP/HTTP JSON logs encoding: just what one batch of product event
// records needs (string attributes, INFO severity).
type otlpValue struct {
	StringValue string `json:"stringValue"`
}

type otlpKV struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpRecord struct {
	TimeUnixNano   string    `json:"timeUnixNano"`
	SeverityNumber int       `json:"severityNumber"`
	SeverityText   string    `json:"severityText"`
	Body           otlpValue `json:"body"`
	Attributes     []otlpKV  `json:"attributes"`
}

type otlpScopeLogs struct {
	Scope struct {
		Name string `json:"name"`
	} `json:"scope"`
	LogRecords []otlpRecord `json:"logRecords"`
}

type otlpResourceLogs struct {
	Resource struct {
		Attributes []otlpKV `json:"attributes"`
	} `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpPayload struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

// scopeName is the instrumentation scope of every record.
const scopeName = "github.com/reliant-labs/forge/pkg/observe/cliev"

func kv(k, v string) otlpKV { return otlpKV{Key: k, Value: otlpValue{StringValue: v}} }

// encode builds the batch's OTLP JSON body.
func (c *Client) encode(batch []record) ([]byte, error) {
	var rl otlpResourceLogs
	rl.Resource.Attributes = []otlpKV{
		kv("service.name", c.cfg.App+"-cli"),
		kv("service.version", c.cfg.Version),
		kv("os.type", runtime.GOOS),
	}
	var sl otlpScopeLogs
	sl.Scope.Name = scopeName
	for _, r := range batch {
		rec := otlpRecord{
			TimeUnixNano:   strconv.FormatInt(r.at.UnixNano(), 10),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Body:           otlpValue{StringValue: observe.EventNamePrefix + r.name},
		}
		for _, a := range r.attrs {
			rec.Attributes = append(rec.Attributes, kv(a.Key, a.Value.Resolve().String()))
		}
		sl.LogRecords = append(sl.LogRecords, rec)
	}
	rl.ScopeLogs = []otlpScopeLogs{sl}
	return json.Marshal(otlpPayload{ResourceLogs: []otlpResourceLogs{rl}})
}

// post sends the batch and reports whether the endpoint accepted it.
func (c *Client) post(ctx context.Context, batch []record) bool {
	body, err := c.encode(batch)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode/100 == 2
}
