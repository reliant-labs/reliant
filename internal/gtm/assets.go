// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"unicode"

	"github.com/oklog/ulid/v2"

	"github.com/reliant-labs/reliant/internal/store"
)

// Asset files. The bytes live in the org's bucket behind AssetStore; the
// service decides every key (gtm/<org_id>/<asset_id>), checks every byte it
// accepts, and fails closed with no store.

// allowedAssetTypes is the allowlist, checked on SNIFFED bytes
// (http.DetectContentType), never on a client's claim. SVG, HTML and XML are
// out: they can carry script.
var allowedAssetTypes = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"video/mp4":       true,
	"video/webm":      true,
	"application/pdf": true,
	"text/plain":      true,
}

// inlineAssetTypes are shown in the browser; everything else downloads as an
// attachment.
var inlineAssetTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"video/mp4":  true,
	"video/webm": true,
}

const (
	maxAssetFilenameLen    = 255
	maxAssetContentTypeLen = 255
	sniffLen               = 512
)

// mediaType is a content type's base, lowercased: "text/plain;
// charset=utf-8" is "text/plain".
func mediaType(ct string) string {
	base, _, _ := strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

// sniff is head's allowed type, or why it is refused. Text that opens like
// markup is refused too: DetectContentType calls an SVG without an XML
// prolog text/plain.
func sniff(head []byte) (string, string) {
	ct := mediaType(http.DetectContentType(head))
	if !allowedAssetTypes[ct] {
		return "", "its type (" + ct + ") is not allowed: png, jpeg, gif, webp, mp4, webm, pdf or plain text"
	}
	if ct == "text/plain" {
		lower := bytes.ToLower(bytes.TrimLeftFunc(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), unicode.IsSpace))
		if bytes.HasPrefix(lower, []byte("<")) || bytes.Contains(lower, []byte("<svg")) || bytes.Contains(lower, []byte("<script")) {
			return "", "markup (SVG, HTML, XML) is not allowed"
		}
	}
	return ct, ""
}

// inspected is what a read of an asset's bytes found.
type inspected struct {
	contentType string
	size        int64
	sha256      string
	// reason is set when the bytes are refused.
	reason string
}

// inspect reads r whole, at most limit bytes: its sniffed type, its size
// and its sha256.
func inspect(r io.Reader, limit int64) (inspected, error) {
	h := sha256.New()
	head := make([]byte, 0, sniffLen)
	var n int64
	buf := make([]byte, 32<<10)
	lr := io.LimitReader(r, limit+1)
	for {
		k, err := lr.Read(buf)
		if k > 0 {
			chunk := buf[:k]
			if room := sniffLen - len(head); room > 0 {
				head = append(head, chunk[:min(room, k)]...)
			}
			h.Write(chunk)
			n += int64(k)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return inspected{}, err
		}
	}
	if n > limit {
		return inspected{reason: "it is larger than allowed"}, nil
	}
	if n == 0 {
		return inspected{reason: "it is empty"}, nil
	}
	ct, reason := sniff(head)
	return inspected{contentType: ct, size: n, sha256: hex.EncodeToString(h.Sum(nil)), reason: reason}, nil
}

// assetKey is where asset id of orgID lives (the table holds it to this).
func assetKey(orgID, id string) string { return "gtm/" + orgID + "/" + id }

// assetSource is who uploads for actor: a person, or a chat's agent.
func assetSource(actor Actor) AssetSource {
	if actor.Person() {
		return AssetSourcePerson
	}
	return AssetSourceChat
}

// cleanFilename keeps a file's last path element, without control
// characters, at most maxAssetFilenameLen.
func cleanFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if name == "." || name == "/" || strings.TrimSpace(name) == "" {
		return "file"
	}
	return clip(name, maxAssetFilenameLen)
}

// checkAssetTarget is exactly one of item and campaign.
func checkAssetTarget(itemID, campaignID string) error {
	if (itemID == "") == (campaignID == "") {
		return invalid("item_id", "exactly one of item_id and campaign_id")
	}
	return nil
}

// checkAssetTargetLink is rule 2 for an asset's target.
func (s *service) checkAssetTargetLink(ctx context.Context, orgID, itemID, campaignID string) error {
	if itemID != "" {
		if _, err := s.store.GetItem(ctx, orgID, itemID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return &LinkError{Field: "item_id", ID: itemID}
			}
			return err
		}
		return nil
	}
	return s.checkCampaignLink(ctx, orgID, campaignID)
}

// ListAssets lists an item's or a campaign's assets, newest first.
func (s *service) ListAssets(ctx context.Context, actor Actor, in ListAssetsInput) (ListAssetsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListAssetsResult{}, err
	}
	if err := checkAssetTarget(in.ItemID, in.CampaignID); err != nil {
		return ListAssetsResult{}, err
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultAssetPageSize, MaxAssetPageSize)
	if err != nil {
		return ListAssetsResult{}, err
	}
	rows, err := s.store.ListAssets(ctx, AssetFilter{OrgID: actor.OrgID, ItemID: in.ItemID, CampaignID: in.CampaignID, Limit: limit, Offset: offset})
	if err != nil {
		return ListAssetsResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	return ListAssetsResult{Assets: assetsFromRows(rows), NextPageToken: next}, nil
}

// GetAsset returns one asset's metadata.
func (s *service) GetAsset(ctx context.Context, actor Actor, in GetAssetInput) (Asset, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Asset{}, err
	}
	if err := checkID("id", in.ID); err != nil {
		return Asset{}, err
	}
	row, err := s.store.GetAsset(ctx, actor.OrgID, in.ID)
	if err != nil {
		return Asset{}, s.notFound(ctx, err, "asset", in.ID)
	}
	return assetFromRow(row), nil
}

// createPending records a PENDING asset of size bytes, within the org's
// quota, and returns its row.
func (s *service) createPending(ctx context.Context, actor Actor, itemID, campaignID, filename, contentType string, size int64, source AssetSource) (*store.GtmAsset, error) {
	id := ulid.Make().String()
	row := &store.GtmAsset{
		ID:          id,
		OrgID:       actor.OrgID,
		ItemID:      strPtrOrNil(itemID),
		CampaignID:  strPtrOrNil(campaignID),
		ObjectKey:   assetKey(actor.OrgID, id),
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   &size,
		Status:      string(AssetPending),
		UploadedBy:  actor.UserID,
		Source:      string(source),
		CreatedAt:   s.now(),
	}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.checkAssetTargetLink(ctx, actor.OrgID, itemID, campaignID); err != nil {
			return err
		}
		used, err := s.store.SumAssetBytes(ctx, actor.OrgID)
		if err != nil {
			return err
		}
		if used+size > MaxOrgAssetBytes {
			return ErrAssetQuota
		}
		r := *row
		return s.store.CreateAsset(ctx, &r)
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

// discard removes an asset's object and row, best effort: a failed check or
// a failed write never leaves either behind on purpose.
func (s *service) discard(ctx context.Context, row *store.GtmAsset) {
	ctx = context.WithoutCancel(ctx)
	if err := s.assets.Delete(ctx, row.ObjectKey); err != nil {
		s.log.WarnContext(ctx, "gtm: deleting a refused asset's object failed", "asset_id", row.ID, "error", err)
	}
	if err := s.store.InTx(ctx, func(ctx context.Context) error { return s.store.DeleteAsset(ctx, row.OrgID, row.ID) }); err != nil {
		s.log.WarnContext(ctx, "gtm: deleting a refused asset's row failed", "asset_id", row.ID, "error", err)
	}
}

// CreateAssetUpload records a PENDING asset and presigns the PUT of its
// bytes.
func (s *service) CreateAssetUpload(ctx context.Context, actor Actor, in CreateAssetUploadInput) (AssetUpload, error) {
	if err := s.admit(ctx, actor); err != nil {
		return AssetUpload{}, err
	}
	if s.assets == nil {
		return AssetUpload{}, ErrAssetsUnavailable
	}
	if err := firstErr(
		checkAssetTarget(in.ItemID, in.CampaignID),
		checkLen("filename", in.Filename, 1, maxAssetFilenameLen),
		checkLen("content_type", in.ContentType, 1, maxAssetContentTypeLen),
	); err != nil {
		return AssetUpload{}, err
	}
	declared := mediaType(in.ContentType)
	if !allowedAssetTypes[declared] {
		return AssetUpload{}, invalid("content_type", "not an allowed type: png, jpeg, gif, webp, mp4, webm, pdf or plain text")
	}
	if in.SizeBytes <= 0 {
		return AssetUpload{}, invalid("size_bytes", "required")
	}
	if in.SizeBytes > MaxAssetBytes {
		return AssetUpload{}, ErrAssetTooLarge
	}
	row, err := s.createPending(ctx, actor, in.ItemID, in.CampaignID, cleanFilename(in.Filename), declared, in.SizeBytes, assetSource(actor))
	if err != nil {
		return AssetUpload{}, err
	}
	req, err := s.assets.PresignPut(ctx, row.ObjectKey, PutOptions{ContentType: declared, Size: in.SizeBytes})
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: presigning an asset upload failed", "asset_id", row.ID, "error", err)
		s.discard(ctx, row)
		return AssetUpload{}, ErrAssetStorageFailed
	}
	return AssetUpload{Asset: assetFromRow(row), UploadURL: req.URL, RequiredHeaders: req.Headers, ExpiresAt: req.ExpiresAt}, nil
}

// ConfirmAssetUpload reads the uploaded object and marks the asset READY,
// or deletes the object and the row.
func (s *service) ConfirmAssetUpload(ctx context.Context, actor Actor, in ConfirmAssetUploadInput) (Asset, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Asset{}, err
	}
	if s.assets == nil {
		return Asset{}, ErrAssetsUnavailable
	}
	if err := checkID("asset_id", in.AssetID); err != nil {
		return Asset{}, err
	}
	row, err := s.store.GetAsset(ctx, actor.OrgID, in.AssetID)
	if err != nil {
		return Asset{}, s.notFound(ctx, err, "asset", in.AssetID)
	}
	if AssetStatus(row.Status) != AssetPending {
		return Asset{}, ErrAssetNotPending
	}
	rc, err := s.assets.Open(ctx, row.ObjectKey)
	if errors.Is(err, ErrObjectNotFound) {
		return Asset{}, ErrAssetNotUploaded
	}
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: opening an uploaded asset failed", "asset_id", row.ID, "error", err)
		return Asset{}, ErrAssetStorageFailed
	}
	declared := int64(0)
	if row.SizeBytes != nil {
		declared = *row.SizeBytes
	}
	got, err := inspect(rc, MaxAssetBytes)
	_ = rc.Close()
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: reading an uploaded asset failed", "asset_id", row.ID, "error", err)
		return Asset{}, ErrAssetStorageFailed
	}
	if got.reason == "" && got.size != declared {
		got.reason = "its size is not the size declared"
	}
	if got.reason != "" {
		s.discard(ctx, row)
		return Asset{}, &AssetRejectedError{Reason: got.reason}
	}
	if err := s.store.InTx(ctx, func(ctx context.Context) error {
		return s.store.MarkAssetReady(ctx, actor.OrgID, row.ID, got.contentType, got.size, got.sha256)
	}); err != nil {
		return Asset{}, err
	}
	stored, err := s.store.GetAsset(ctx, actor.OrgID, row.ID)
	if err != nil {
		return Asset{}, s.notFound(ctx, err, "asset", row.ID)
	}
	return assetFromRow(stored), nil
}

// GetAssetDownload presigns a READY asset's GET: inline for raster images
// and mp4/webm, an attachment for everything else.
func (s *service) GetAssetDownload(ctx context.Context, actor Actor, in GetAssetDownloadInput) (AssetDownload, error) {
	if err := s.admit(ctx, actor); err != nil {
		return AssetDownload{}, err
	}
	if s.assets == nil {
		return AssetDownload{}, ErrAssetsUnavailable
	}
	if err := checkID("asset_id", in.AssetID); err != nil {
		return AssetDownload{}, err
	}
	row, err := s.store.GetAsset(ctx, actor.OrgID, in.AssetID)
	if err != nil {
		return AssetDownload{}, s.notFound(ctx, err, "asset", in.AssetID)
	}
	if AssetStatus(row.Status) != AssetReady {
		return AssetDownload{}, ErrAssetNotReady
	}
	ct := mediaType(row.ContentType)
	req, err := s.assets.PresignGet(ctx, row.ObjectKey, GetOptions{Inline: inlineAssetTypes[ct], ContentType: ct, Filename: row.Filename})
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: presigning an asset download failed", "asset_id", row.ID, "error", err)
		return AssetDownload{}, ErrAssetStorageFailed
	}
	return AssetDownload{URL: req.URL, ExpiresAt: req.ExpiresAt}, nil
}

// AttachFromAttachment copies one of the actor's chat attachments into a
// READY asset, with ConfirmAssetUpload's checks and MaxCopiedAssetBytes.
func (s *service) AttachFromAttachment(ctx context.Context, actor Actor, in AttachFromAttachmentInput) (Asset, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Asset{}, err
	}
	if s.assets == nil {
		return Asset{}, ErrAssetsUnavailable
	}
	if s.attachments == nil {
		return Asset{}, ErrAttachmentsUnavailable
	}
	if err := firstErr(checkID("attachment_id", in.AttachmentID), checkAssetTarget(in.ItemID, in.CampaignID)); err != nil {
		return Asset{}, err
	}
	att, rc, err := s.attachments.Open(ctx, actor, in.AttachmentID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Asset{}, s.notFound(ctx, err, "attachment", in.AttachmentID)
		}
		s.log.ErrorContext(ctx, "gtm: opening a chat attachment failed", "attachment_id", in.AttachmentID, "error", err)
		return Asset{}, ErrAttachmentsUnavailable
	}
	if att.Size > MaxCopiedAssetBytes {
		_ = rc.Close()
		return Asset{}, ErrAssetTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(rc, MaxCopiedAssetBytes+1))
	_ = rc.Close()
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: reading a chat attachment failed", "attachment_id", in.AttachmentID, "error", err)
		return Asset{}, ErrAttachmentsUnavailable
	}
	if int64(len(data)) > MaxCopiedAssetBytes {
		return Asset{}, ErrAssetTooLarge
	}
	got, err := inspect(bytes.NewReader(data), MaxCopiedAssetBytes)
	if err != nil {
		return Asset{}, err
	}
	if got.reason != "" {
		return Asset{}, &AssetRejectedError{Reason: got.reason}
	}
	row, err := s.createPending(ctx, actor, in.ItemID, in.CampaignID, cleanFilename(att.Filename), got.contentType, got.size, AssetSourceChat)
	if err != nil {
		return Asset{}, err
	}
	if err := s.assets.Put(ctx, row.ObjectKey, bytes.NewReader(data), PutOptions{ContentType: got.contentType, Size: got.size}); err != nil {
		s.log.ErrorContext(ctx, "gtm: writing a copied asset failed", "asset_id", row.ID, "error", err)
		s.discard(ctx, row)
		return Asset{}, ErrAssetStorageFailed
	}
	if err := s.store.InTx(ctx, func(ctx context.Context) error {
		return s.store.MarkAssetReady(ctx, actor.OrgID, row.ID, got.contentType, got.size, got.sha256)
	}); err != nil {
		s.discard(ctx, row)
		return Asset{}, err
	}
	stored, err := s.store.GetAsset(ctx, actor.OrgID, row.ID)
	if err != nil {
		return Asset{}, s.notFound(ctx, err, "asset", row.ID)
	}
	return assetFromRow(stored), nil
}
