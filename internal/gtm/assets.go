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
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"github.com/reliant-labs/reliant/internal/store"
)

// Asset files. The bytes live in the org's bucket behind AssetStore; the
// service decides every key (a client uploads to gtm/<org_id>/<asset_id>.upload,
// and only the service writes gtm/<org_id>/<asset_id>), checks every byte it
// serves, and fails closed with no store.

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

	// assetPendingGrace is how long a PENDING asset outlives its upload
	// URL: an upload that starts as the URL expires still lands, and is
	// confirmed, before the asset expires.
	assetPendingGrace = 5 * time.Minute
	// assetPresignSkew is how far past AssetUploadTTL an AssetStore's
	// clock may put a presigned PUT's expiry; a later one is refused.
	assetPresignSkew = time.Minute
	// maxAssetReap caps the expired assets one call deletes.
	maxAssetReap = 20
)

// mediaType is a content type's base, lowercased: "text/plain;
// charset=utf-8" is "text/plain".
func mediaType(ct string) string {
	base, _, _ := strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

// sniff is data's allowed type, or why it is refused.
func sniff(data []byte) (string, string) {
	ct := mediaType(http.DetectContentType(data))
	if !allowedAssetTypes[ct] {
		return "", "its type (" + ct + ") is not allowed: png, jpeg, gif, webp, mp4, webm, pdf or plain text"
	}
	if ct == "text/plain" {
		return ct, checkText(data)
	}
	return ct, ""
}

var (
	utf8BOM = []byte("\xef\xbb\xbf")
	// markupTags open markup a browser may render; matched anywhere in
	// plain text, case-insensitively.
	markupTags = [][]byte{[]byte("<svg"), []byte("<script"), []byte("<html"), []byte("<?xml"), []byte("<!doctype")}
)

// checkText is why plain text is refused, or "": text that is not UTF-8
// (UTF-16, with its byte order mark, included), and markup anywhere in it,
// since DetectContentType reads only the first 512 bytes and calls an SVG
// without an XML prolog text/plain.
func checkText(data []byte) string {
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return "text must be UTF-8"
	}
	body := bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(body) {
		return "text must be UTF-8"
	}
	if bytes.HasPrefix(bytes.TrimLeftFunc(body, unicode.IsSpace), []byte("<")) || hasMarkup(body) {
		return "markup (SVG, HTML, XML) is not allowed"
	}
	return ""
}

// hasMarkup reports whether b holds one of markupTags anywhere.
func hasMarkup(b []byte) bool {
	for {
		i := bytes.IndexByte(b, '<')
		if i < 0 {
			return false
		}
		b = b[i:]
		for _, tag := range markupTags {
			if len(b) >= len(tag) && bytes.EqualFold(b[:len(tag)], tag) {
				return true
			}
		}
		b = b[1:]
	}
}

// inspected is what a check of an asset's bytes found.
type inspected struct {
	contentType string
	size        int64
	sha256      string
	// reason is set when the bytes are refused.
	reason string
}

// inspect checks data, at most limit bytes: its sniffed type, its size and
// its sha256.
func inspect(data []byte, limit int64) inspected {
	n := int64(len(data))
	if n > limit {
		return inspected{reason: "it is larger than allowed"}
	}
	if n == 0 {
		return inspected{reason: "it is empty"}
	}
	ct, reason := sniff(data)
	sum := sha256.Sum256(data)
	return inspected{contentType: ct, size: n, sha256: hex.EncodeToString(sum[:]), reason: reason}
}

// readAll reads r whole into a buffer sized for size bytes, stopping past
// limit (the caller checks the length).
func readAll(r io.Reader, size, limit int64) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, min(size, limit)+1))
	if _, err := buf.ReadFrom(io.LimitReader(r, limit+1)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// assetKey is the asset's own key, where its checked bytes live (the table
// holds its row to it). Only the service writes it.
func assetKey(orgID, id string) string { return "gtm/" + orgID + "/" + id }

// stagingKey is where a client uploads the bytes of the asset whose own key
// is objectKey, for ConfirmAssetUpload to check.
func stagingKey(objectKey string) string { return objectKey + ".upload" }

// pendingCutoff is the creation time at or before which a PENDING asset
// has expired.
func (s *service) pendingCutoff() time.Time {
	return s.now().Add(-(AssetUploadTTL + assetPendingGrace))
}

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
// quota (READY assets and PENDING ones created after cutoff), and returns
// its row.
func (s *service) createPending(ctx context.Context, actor Actor, itemID, campaignID, filename, contentType string, size int64, source AssetSource, cutoff time.Time) (*store.GtmAsset, error) {
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
		used, err := s.store.SumAssetBytes(ctx, actor.OrgID, cutoff)
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

// deleteObjects deletes an asset's staging object and its own.
func (s *service) deleteObjects(ctx context.Context, row *store.GtmAsset) error {
	var errs []error
	for _, key := range []string{stagingKey(row.ObjectKey), row.ObjectKey} {
		if err := s.assets.Delete(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// discard removes a PENDING asset, best effort: a failed check, a failed
// write or an expiry never leaves it behind on purpose. The row goes first,
// and only while it is still PENDING (and, when before is set, created at
// or before it), so a confirm that won a race keeps its READY asset; then
// its objects.
func (s *service) discard(ctx context.Context, row *store.GtmAsset, before time.Time) {
	ctx = context.WithoutCancel(ctx)
	var deleted bool
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		var err error
		deleted, err = s.store.DeletePendingAsset(ctx, row.OrgID, row.ID, before)
		return err
	})
	if err != nil {
		s.log.WarnContext(ctx, "gtm: deleting a PENDING asset's row failed", "asset_id", row.ID, "error", err)
		return
	}
	if !deleted {
		return
	}
	if err := s.deleteObjects(ctx, row); err != nil {
		s.log.WarnContext(ctx, "gtm: deleting a discarded asset's objects failed; they are orphaned", "asset_id", row.ID, "object_key", row.ObjectKey, "error", err)
	}
}

// remove deletes an asset whatever its status: its row first, then its
// objects. Row first, so a confirm racing the delete sees the row gone once
// its own write lands and deletes what it wrote (ConfirmAssetUpload step
// 5); bytes are never left behind by the race. A storage failure after the
// row is gone orphans the objects: it is logged with their keys, and the
// asset is gone all the same.
func (s *service) remove(ctx context.Context, row *store.GtmAsset) error {
	if err := s.store.InTx(ctx, func(ctx context.Context) error { return s.store.DeleteAsset(ctx, row.OrgID, row.ID) }); err != nil {
		return err
	}
	if err := s.deleteObjects(context.WithoutCancel(ctx), row); err != nil {
		s.log.ErrorContext(ctx, "gtm: deleting a deleted asset's objects failed; they are orphaned", "asset_id", row.ID, "object_key", row.ObjectKey, "error", err)
	}
	return nil
}

// reap deletes up to maxAssetReap of orgID's expired PENDING assets, best
// effort: an upload whose client never confirmed it.
func (s *service) reap(ctx context.Context, orgID string, cutoff time.Time) {
	ctx = context.WithoutCancel(ctx)
	rows, err := s.store.ListExpiredAssets(ctx, orgID, cutoff, maxAssetReap)
	if err != nil {
		s.log.WarnContext(ctx, "gtm: listing expired assets failed", "error", err)
		return
	}
	for _, row := range rows {
		s.discard(ctx, row, cutoff)
	}
}

// CreateAssetUpload records a PENDING asset and presigns the PUT of its
// bytes to its staging object, for at most AssetUploadTTL.
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
	cutoff := s.pendingCutoff()
	s.reap(ctx, actor.OrgID, cutoff)
	row, err := s.createPending(ctx, actor, in.ItemID, in.CampaignID, cleanFilename(in.Filename), declared, in.SizeBytes, assetSource(actor), cutoff)
	if err != nil {
		return AssetUpload{}, err
	}
	req, err := s.assets.PresignPut(ctx, stagingKey(row.ObjectKey), PutOptions{ContentType: declared, Size: in.SizeBytes, TTL: AssetUploadTTL})
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: presigning an asset upload failed", "asset_id", row.ID, "error", err)
		s.discard(ctx, row, time.Time{})
		return AssetUpload{}, ErrAssetStorageFailed
	}
	// The URL must die before the row expires, or a late upload would land
	// on an asset the quota no longer counts.
	if req.ExpiresAt.IsZero() || req.ExpiresAt.After(row.CreatedAt.Add(AssetUploadTTL+assetPresignSkew)) {
		s.log.ErrorContext(ctx, "gtm: the asset store presigned an upload past AssetUploadTTL", "asset_id", row.ID, "expires_at", req.ExpiresAt)
		s.discard(ctx, row, time.Time{})
		return AssetUpload{}, ErrAssetStorageFailed
	}
	return AssetUpload{Asset: assetFromRow(row), UploadURL: req.URL, RequiredHeaders: req.Headers, ExpiresAt: req.ExpiresAt}, nil
}

// ConfirmAssetUpload reads the staging object once, checks those bytes,
// and makes them the asset:
//
//  1. An expired PENDING asset is deleted (ErrAssetUploadExpired); the
//     org's other expired ones go too.
//  2. The staging object is read whole into memory (at most MaxAssetBytes)
//     and checked: size against the declared, sniffed type, sha256.
//  3. The row is marked READY with what the check found. Only the confirm
//     that moves it from PENDING goes on, so two confirms racing never
//     write one's bytes under the other's sha256.
//  4. Exactly the bytes checked are written to the asset's own key, which
//     no client can write, so a re-upload to the staging object after the
//     check changes nothing served. The staging object is deleted.
//  5. The row is read again. A DeleteAsset that ran meanwhile deleted it
//     first and the objects after, perhaps before this write landed: with
//     the row gone, this call deletes the objects itself (ErrNotFound).
//
// A refusal or a failure deletes both objects and the row, except a
// staging object not uploaded yet (ErrAssetNotUploaded), which leaves the
// asset PENDING. A crash between 3 and 4 leaves a READY row with no bytes,
// which GetAssetDownload refuses (ErrAssetNotReady) and DeleteAsset
// removes.
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
	cutoff := s.pendingCutoff()
	if !row.CreatedAt.After(cutoff) {
		s.discard(ctx, row, cutoff)
		return Asset{}, ErrAssetUploadExpired
	}
	s.reap(ctx, actor.OrgID, cutoff)

	staging := stagingKey(row.ObjectKey)
	rc, err := s.assets.Open(ctx, staging)
	if errors.Is(err, ErrObjectNotFound) {
		return Asset{}, ErrAssetNotUploaded
	}
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: opening an uploaded asset failed", "asset_id", row.ID, "error", err)
		s.discard(ctx, row, time.Time{})
		return Asset{}, ErrAssetStorageFailed
	}
	declared := int64(0)
	if row.SizeBytes != nil {
		declared = *row.SizeBytes
	}
	data, err := readAll(rc, declared, MaxAssetBytes)
	_ = rc.Close()
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: reading an uploaded asset failed", "asset_id", row.ID, "error", err)
		s.discard(ctx, row, time.Time{})
		return Asset{}, ErrAssetStorageFailed
	}
	got := inspect(data, MaxAssetBytes)
	if got.reason == "" && got.size != declared {
		got.reason = "its size is not the size declared"
	}
	if got.reason != "" {
		s.discard(ctx, row, time.Time{})
		return Asset{}, &AssetRejectedError{Reason: got.reason}
	}

	// The cutoff again, now: the read may have outlasted the asset's window,
	// and an expired asset no longer counts toward the quota.
	if err := s.store.InTx(ctx, func(ctx context.Context) error {
		cutoff = s.pendingCutoff()
		return s.store.MarkAssetReady(ctx, actor.OrgID, row.ID, got.contentType, got.size, got.sha256, cutoff)
	}); err != nil {
		if errors.Is(err, ErrAssetNotPending) {
			if !row.CreatedAt.After(cutoff) {
				s.discard(ctx, row, cutoff)
				return Asset{}, ErrAssetUploadExpired
			}
			// Another confirm won, or the asset was deleted: not this
			// call's to touch.
			return Asset{}, err
		}
		s.discard(ctx, row, time.Time{})
		return Asset{}, err
	}
	detached := context.WithoutCancel(ctx)
	if err := s.assets.Put(detached, row.ObjectKey, bytes.NewReader(data), PutOptions{ContentType: got.contentType, Size: got.size}); err != nil {
		s.log.ErrorContext(ctx, "gtm: writing a confirmed asset failed", "asset_id", row.ID, "error", err)
		if rmErr := s.remove(detached, row); rmErr != nil {
			s.log.WarnContext(ctx, "gtm: removing an asset whose write failed failed", "asset_id", row.ID, "error", rmErr)
		}
		return Asset{}, ErrAssetStorageFailed
	}
	if err := s.assets.Delete(detached, staging); err != nil {
		s.log.WarnContext(ctx, "gtm: deleting a confirmed asset's staging object failed", "asset_id", row.ID, "object_key", staging, "error", err)
	}
	stored, err := s.store.GetAsset(detached, actor.OrgID, row.ID)
	if errors.Is(err, ErrNotFound) {
		// Deleted while this call wrote: its bytes go too.
		if err := s.deleteObjects(detached, row); err != nil {
			s.log.ErrorContext(ctx, "gtm: deleting a deleted asset's objects failed; they are orphaned", "asset_id", row.ID, "object_key", row.ObjectKey, "error", err)
		}
	}
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
	// A READY row whose bytes are gone (a crash between confirm's mark and
	// its write, a bucket-side delete) is not served: a HEAD, not a read.
	if _, err := s.assets.Stat(ctx, row.ObjectKey); err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			s.log.WarnContext(ctx, "gtm: a READY asset's object is missing", "asset_id", row.ID, "object_key", row.ObjectKey)
			return AssetDownload{}, ErrAssetNotReady
		}
		s.log.ErrorContext(ctx, "gtm: checking an asset's object failed", "asset_id", row.ID, "error", err)
		return AssetDownload{}, ErrAssetStorageFailed
	}
	ct := mediaType(row.ContentType)
	req, err := s.assets.PresignGet(ctx, row.ObjectKey, GetOptions{Inline: inlineAssetTypes[ct], ContentType: ct, Filename: row.Filename})
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: presigning an asset download failed", "asset_id", row.ID, "error", err)
		return AssetDownload{}, ErrAssetStorageFailed
	}
	return AssetDownload{URL: req.URL, ExpiresAt: req.ExpiresAt}, nil
}

// DeleteAsset deletes an asset in any status, a person's call: its row,
// then its objects (see remove).
func (s *service) DeleteAsset(ctx context.Context, actor Actor, in DeleteAssetInput) error {
	if err := s.admit(ctx, actor); err != nil {
		return err
	}
	if !actor.Person() {
		return personOnly("delete an asset")
	}
	if s.assets == nil {
		return ErrAssetsUnavailable
	}
	if err := checkID("asset_id", in.AssetID); err != nil {
		return err
	}
	row, err := s.store.GetAsset(ctx, actor.OrgID, in.AssetID)
	if err != nil {
		return s.notFound(ctx, err, "asset", in.AssetID)
	}
	return s.remove(ctx, row)
}

// AttachFromAttachment copies one of the actor's chat attachments into a
// READY asset, with ConfirmAssetUpload's checks and MaxCopiedAssetBytes.
// The bytes are read once and written as checked, straight to the asset's
// own key.
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
	data, err := readAll(rc, max(att.Size, 0), MaxCopiedAssetBytes)
	_ = rc.Close()
	if err != nil {
		s.log.ErrorContext(ctx, "gtm: reading a chat attachment failed", "attachment_id", in.AttachmentID, "error", err)
		return Asset{}, ErrAttachmentsUnavailable
	}
	if int64(len(data)) > MaxCopiedAssetBytes {
		return Asset{}, ErrAssetTooLarge
	}
	got := inspect(data, MaxCopiedAssetBytes)
	if got.reason != "" {
		return Asset{}, &AssetRejectedError{Reason: got.reason}
	}
	cutoff := s.pendingCutoff()
	row, err := s.createPending(ctx, actor, in.ItemID, in.CampaignID, cleanFilename(att.Filename), got.contentType, got.size, AssetSourceChat, cutoff)
	if err != nil {
		return Asset{}, err
	}
	if err := s.assets.Put(ctx, row.ObjectKey, bytes.NewReader(data), PutOptions{ContentType: got.contentType, Size: got.size}); err != nil {
		s.log.ErrorContext(ctx, "gtm: writing a copied asset failed", "asset_id", row.ID, "error", err)
		s.discard(ctx, row, time.Time{})
		return Asset{}, ErrAssetStorageFailed
	}
	if err := s.store.InTx(ctx, func(ctx context.Context) error {
		return s.store.MarkAssetReady(ctx, actor.OrgID, row.ID, got.contentType, got.size, got.sha256, cutoff)
	}); err != nil {
		s.discard(ctx, row, time.Time{})
		return Asset{}, err
	}
	stored, err := s.store.GetAsset(ctx, actor.OrgID, row.ID)
	if err != nil {
		return Asset{}, s.notFound(ctx, err, "asset", row.ID)
	}
	return assetFromRow(stored), nil
}
