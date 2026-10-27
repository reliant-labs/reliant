package reviewpublisheradapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
)

// Publisher persists a one-way create claim before CP HTTP. Retries reconcile;
// neither absence nor an unknown result permits a blind create retry.
type Publisher struct {
	Store  store.ReviewAuthorityStore
	Heads  reviewpublisher.HeadSource
	Bridge CheckBridge
}

// Publish revalidates SQL authority and current provider SHA, then creates once
// or reconciles. A confirmed nonsecret check receipt is persisted before return.
func (p Publisher) Publish(ctx context.Context, id string) (CheckReceipt, error) {
	if p.Heads == nil || p.Bridge == nil {
		return CheckReceipt{}, reviewpublisher.ErrDisabled
	}
	intent, a, err := p.Store.Publication(ctx, id)
	if err != nil {
		return CheckReceipt{}, err
	}
	head, err := p.Heads.CurrentHead(ctx, a.Binding.RepositoryID, a.Binding.PullNumber)
	if err != nil {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	if head.RepositoryID != a.Binding.RepositoryID || head.PullNumber != a.Binding.PullNumber || head.SHA != a.Binding.HeadSHA {
		return CheckReceipt{}, reviewpublisher.ErrUnauthorized
	}
	if intent.State == "confirmed" {
		return CheckReceipt{ID: intent.CheckID, URL: intent.CheckURL}, nil
	}
	create, err := p.Store.ClaimPublication(ctx, id)
	if err != nil {
		return CheckReceipt{}, err
	}
	// Re-read after claim: a policy/head/generation change must not be concealed
	// by the previously loaded intent. This still is not an atomic DB/GitHub fence.
	intent, a, err = p.Store.Publication(ctx, id)
	if err != nil {
		return CheckReceipt{}, err
	}
	owner, repo, ok := strings.Cut(a.RepoName, "/")
	if !ok {
		return CheckReceipt{}, reviewpublisher.ErrUnauthorized
	}
	request := CheckRequest{OwnerID: a.OwnerID, RepositoryID: a.Binding.RepositoryID, Owner: owner, Repo: repo, HeadSHA: a.Binding.HeadSHA, Name: intent.Intent.CheckName, ExternalID: "review:" + a.ID + ":" + strconv.FormatInt(a.Binding.Generation, 10) + ":" + intent.ID, RunURL: intent.Intent.DetailsURL, Summary: intent.Intent.Summary, Conclusion: intent.Intent.Conclusion}
	var receipt CheckReceipt
	if create {
		receipt, err = p.Bridge.Publish(ctx, request)
	} else {
		receipt, err = p.Bridge.Reconcile(ctx, request)
	}
	if err != nil || receipt.ID <= 0 || receipt.URL == "" {
		_ = p.Store.UncertainPublication(ctx, id)
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	if err = p.Store.ConfirmPublication(ctx, id, store.ReviewCheckReceipt{RepositoryID: request.RepositoryID, HeadSHA: request.HeadSHA, Name: request.Name, ExternalID: request.ExternalID, CheckID: receipt.ID, CheckURL: receipt.URL}); err != nil {
		return CheckReceipt{}, err
	}
	return receipt, nil
}

// ServiceAuth applies an internal-service credential at call time, never returns
// one to a graph/model. The shared service fleet remains the trust boundary.
type ServiceAuth interface{ Apply(*http.Request) error }

// BridgeHTTP is the fixed CP1421 JSON wire client while that contract is not yet
// in Reliant's generated CP pin. It is explicitly constructed with internal auth;
// no generic HTTP tool, token mint, arbitrary method/path or GitHub call exists.
type BridgeHTTP struct {
	Origin string
	Client *http.Client
	Auth   ServiceAuth
}

const bridgeService = "/controlplane.v1.GitCredentialInternalService/"

// Publish calls only the fixed nonsecret internal publish RPC.
func (b BridgeHTTP) Publish(ctx context.Context, in CheckRequest) (CheckReceipt, error) {
	return b.call(ctx, "PublishReviewCheck", in, false)
}

// Reconcile observes exact identity; NOT_FOUND/UNKNOWN are never successful.
func (b BridgeHTTP) Reconcile(ctx context.Context, in CheckRequest) (CheckReceipt, error) {
	return b.call(ctx, "ReconcileReviewCheck", in, true)
}

func (b BridgeHTTP) call(ctx context.Context, method string, in CheckRequest, reconcile bool) (CheckReceipt, error) {
	u, err := url.Parse(b.Origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Hostname() == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) || b.Client == nil || b.Auth == nil {
		return CheckReceipt{}, reviewpublisher.ErrDisabled
	}
	request := map[string]any{"userId": in.OwnerID, "repositoryId": strconv.FormatInt(in.RepositoryID, 10), "owner": in.Owner, "repo": in.Repo, "headSha": in.HeadSHA, "name": in.Name, "externalId": in.ExternalID, "runUrl": in.RunURL}
	if !reconcile {
		conclusion := "REVIEW_CHECK_CONCLUSION_FAILURE"
		if in.Conclusion == "success" {
			conclusion = "REVIEW_CHECK_CONCLUSION_SUCCESS"
		} else if in.Conclusion != "failure" {
			return CheckReceipt{}, reviewpublisher.ErrUnauthorized
		}
		request["status"] = "REVIEW_CHECK_STATUS_COMPLETED"
		request["conclusion"] = conclusion
		request["summary"] = in.Summary
		request["checkId"] = strconv.FormatInt(in.CheckID, 10)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return CheckReceipt{}, reviewpublisher.ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(b.Origin, "/")+bridgeService+method, bytes.NewReader(raw))
	if err != nil {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	if b.Auth.Apply(req) != nil {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	client := *b.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 16385))
	if err != nil || len(data) > 16384 {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	var out struct {
		CheckID  json.Number `json:"checkId"`
		CheckURL string      `json:"checkUrl"`
		State    string      `json:"state"`
	}
	if json.Unmarshal(data, &out) != nil || (reconcile && out.State != "REVIEW_CHECK_RECONCILE_STATE_FOUND") {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	id, err := strconv.ParseInt(string(out.CheckID), 10, 64)
	if err != nil || id <= 0 || len(out.CheckURL) > 2048 {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	checkURL, err := url.Parse(out.CheckURL)
	if err != nil || checkURL.Scheme != "https" || checkURL.Host != "github.com" || checkURL.User != nil || checkURL.RawQuery != "" || checkURL.Fragment != "" || out.CheckURL != fmt.Sprintf("https://github.com/%s/%s/runs/%d", in.Owner, in.Repo, id) {
		return CheckReceipt{}, reviewpublisher.ErrUnavailable
	}
	return CheckReceipt{ID: id, URL: out.CheckURL}, nil
}
