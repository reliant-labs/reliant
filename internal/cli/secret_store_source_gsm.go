package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
)

// Google Secret Manager, through Application Default Credentials.
//
// GCPSecretManagerStore is read by ESO over Workload Identity; the person who
// WRITES to it uses their own ADC (`gcloud auth application-default login`, a
// service account key in GOOGLE_APPLICATION_CREDENTIALS, or the metadata
// server on GCE). The same client backs `secret import --to-bootstrap` (the
// BootstrapSecret containers) and `env secrets verify --against gsm:`.
//
// GSM has no check-and-set. A create-only write is "the secret has no usable
// version, then add one": two importers racing could both pass the check. The
// bootstrap set is written by one operator at a time, and the second writer's
// value becomes version 2 with version 1 intact, so nothing is lost; it is the
// weaker guarantee the design accepts for GSM.

// gsmAPI is the slice of the Secret Manager REST API the commands use. No
// method returns a value except Access, which only `verify` calls and which
// hands the bytes straight to a digest comparison.
type gsmAPI interface {
	// Latest reports the newest version of a secret: its number, whether it is
	// usable (enabled), and whether the secret exists at all.
	Latest(ctx context.Context, project, id string) (version uint32, usable, exists bool, err error)
	// AddVersion adds a version, creating the secret first when create is set
	// and it does not exist.
	AddVersion(ctx context.Context, project, id string, value []byte, create bool) (version uint32, err error)
	// Access returns the newest enabled version's payload.
	Access(ctx context.Context, project, id string) ([]byte, error)
	// List returns the ids of the project's secrets.
	List(ctx context.Context, project string) ([]string, error)
	// Disable takes one version out of service. Only the bootstrap mint calls
	// it, on the version it just added when another writer got there first.
	Disable(ctx context.Context, project, id string, version uint32) error
}

// newGSMAPI is the seam commands open Secret Manager through.
var newGSMAPI = func(ctx context.Context) (gsmAPI, error) {
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
	})
	if err != nil {
		return nil, fmt.Errorf("no Google Application Default Credentials: %w\n"+
			"fix: gcloud auth application-default login   (or set GOOGLE_APPLICATION_CREDENTIALS)", err)
	}
	client, err := httptransport.NewClient(&httptransport.Options{Credentials: creds})
	if err != nil {
		return nil, fmt.Errorf("google http client: %w", err)
	}
	return &gsmREST{client: client, base: "https://secretmanager.googleapis.com/v1"}, nil
}

type gsmREST struct {
	client *http.Client
	base   string
}

func (g *gsmREST) call(ctx context.Context, method, path string, query url.Values, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	u := g.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, err
}

// gsmError renders a Secret Manager error response: its status and message,
// which name resources and never echo a request payload.
func gsmError(op string, status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := e.Error.Message
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("secret manager %s: HTTP %d %s: %s", op, status, e.Error.Status, msg)
}

func gsmSecretPath(project, id string) string {
	return "/projects/" + url.PathEscape(project) + "/secrets/" + url.PathEscape(id)
}

func (g *gsmREST) Latest(ctx context.Context, project, id string) (uint32, bool, bool, error) {
	status, body, err := g.call(ctx, http.MethodGet, gsmSecretPath(project, id)+"/versions/latest", nil, nil)
	if err != nil {
		return 0, false, false, err
	}
	switch status {
	case http.StatusOK:
		var v struct {
			Name  string `json:"name"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return 0, false, false, err
		}
		n, _ := strconv.ParseUint(v.Name[strings.LastIndex(v.Name, "/")+1:], 10, 32)
		return uint32(n), v.State == "ENABLED", true, nil
	case http.StatusNotFound:
		// 404 is "no such version" or "no such secret"; tell them apart so a
		// container with no version yet is not reported as absent.
		st, b, err := g.call(ctx, http.MethodGet, gsmSecretPath(project, id), nil, nil)
		if err != nil {
			return 0, false, false, err
		}
		switch st {
		case http.StatusOK:
			return 0, false, true, nil
		case http.StatusNotFound:
			return 0, false, false, nil
		default:
			return 0, false, false, gsmError("get secret", st, b)
		}
	}
	return 0, false, false, gsmError("get latest version", status, body)
}

func (g *gsmREST) AddVersion(ctx context.Context, project, id string, value []byte, create bool) (uint32, error) {
	add := func() (int, []byte, error) {
		return g.call(ctx, http.MethodPost, gsmSecretPath(project, id)+":addVersion", nil,
			map[string]any{"payload": map[string]any{"data": base64.StdEncoding.EncodeToString(value)}})
	}
	status, body, err := add()
	if err != nil {
		return 0, err
	}
	if status == http.StatusNotFound && create {
		st, b, err := g.call(ctx, http.MethodPost, "/projects/"+url.PathEscape(project)+"/secrets",
			url.Values{"secretId": {id}}, map[string]any{"replication": map[string]any{"automatic": map[string]any{}}})
		if err != nil {
			return 0, err
		}
		if st != http.StatusOK && st != http.StatusConflict {
			return 0, gsmError("create secret", st, b)
		}
		status, body, err = add()
		if err != nil {
			return 0, err
		}
	}
	if status != http.StatusOK {
		return 0, gsmError("add version", status, body)
	}
	var v struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return 0, err
	}
	n, _ := strconv.ParseUint(v.Name[strings.LastIndex(v.Name, "/")+1:], 10, 32)
	return uint32(n), nil
}

func (g *gsmREST) Access(ctx context.Context, project, id string) ([]byte, error) {
	status, body, err := g.call(ctx, http.MethodGet, gsmSecretPath(project, id)+"/versions/latest:access", nil, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, gsmError("access", status, body)
	}
	var p struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(p.Payload.Data)
}

func (g *gsmREST) Disable(ctx context.Context, project, id string, version uint32) error {
	path := gsmSecretPath(project, id) + "/versions/" + strconv.FormatUint(uint64(version), 10) + ":disable"
	status, body, err := g.call(ctx, http.MethodPost, path, nil, map[string]any{})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return gsmError("disable version", status, body)
	}
	return nil
}

func (g *gsmREST) List(ctx context.Context, project string) ([]string, error) {
	var ids []string
	token := ""
	for {
		q := url.Values{"pageSize": {"250"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		status, body, err := g.call(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/secrets", q, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, gsmError("list secrets", status, body)
		}
		var page struct {
			Secrets []struct {
				Name string `json:"name"`
			} `json:"secrets"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		for _, s := range page.Secrets {
			ids = append(ids, s.Name[strings.LastIndex(s.Name, "/")+1:])
		}
		if page.NextPageToken == "" {
			return ids, nil
		}
		token = page.NextPageToken
	}
}

// gsmStoreSource is a Google Secret Manager project as a store.
type gsmStoreSource struct {
	api     gsmAPI
	project string
	// create lets a write create the secret (container) when it is absent.
	create bool
}

func (g gsmStoreSource) describe() string { return "Google Secret Manager project " + g.project }

func (g gsmStoreSource) put(ctx context.Context, name, value string, createOnly bool) (uint32, error) {
	if createOnly {
		_, usable, exists, err := g.api.Latest(ctx, g.project, name)
		if err != nil {
			return 0, err
		}
		if exists && usable {
			return 0, errStoreValueExists
		}
	}
	v, err := g.api.AddVersion(ctx, g.project, name, []byte(value), g.create)
	if err != nil {
		return 0, redactValue(err, value)
	}
	return v, nil
}

func (g gsmStoreSource) presence(ctx context.Context, names []string) (map[string]uint32, error) {
	versions := map[string]uint32{}
	for _, n := range names {
		v, usable, exists, err := g.api.Latest(ctx, g.project, n)
		if err != nil {
			return nil, err
		}
		if exists && usable {
			versions[n] = v
		}
	}
	return versions, nil
}
