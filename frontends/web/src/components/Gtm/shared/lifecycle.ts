// Copyright (c) 2026 Reliant Labs

/**
 * What a PERSON may do to an item, by its status: the lifecycle on
 * GtmItemStatus (proto/reliant/v1/gtm.proto), which the service enforces and
 * these mirror so the pages offer only what it would accept.
 *
 *   - DRAFT or REVIEW → APPROVED;
 *   - APPROVED → SCHEDULED, for an item that is not email-type and has a date;
 *   - any status but SENT → PAUSED, and PAUSED → DRAFT;
 *   - APPROVED or SCHEDULED → PUBLISHED, for an item that is not email-type,
 *     posted by hand on a MANUAL channel: it records where it went out
 *     (published_url) in the same write;
 *   - a content edit of an APPROVED or SCHEDULED item returns it to REVIEW;
 *   - SENT is the email send path's alone, and a SENT item is read-only.
 *
 * Email-type means format EMAIL or COLD_OUTREACH: never SCHEDULED or
 * PUBLISHED. A HELD email (approval_hash set while it is not SENT: a send
 * whose outcome is unknown) is frozen: every edit is refused until a person
 * resolves the send (ResolveItemSend). Nothing here publishes or sends.
 */

import { GtmItemStatus, type GtmChannel, type GtmItem } from "@/gen/reliant/v1/gtm_pb";

import { isEmailFormat, isManualChannel } from "./ui";

export const isEmailItem = (item: GtmItem) => isEmailFormat(item.format);

/** A send of this email started and its outcome is unknown: frozen until a person resolves it. */
export const isHeld = (item: GtmItem) => item.approvalHash !== "" && item.status !== GtmItemStatus.SENT;

const out = (item: GtmItem) => item.status === GtmItemStatus.SENT || item.status === GtmItemStatus.PUBLISHED;

export const canApprove = (item: GtmItem) => !isHeld(item) && (item.status === GtmItemStatus.DRAFT || item.status === GtmItemStatus.REVIEW);

export const canSchedule = (item: GtmItem) => !isHeld(item) && !isEmailItem(item) && item.status === GtmItemStatus.APPROVED && item.scheduledAt !== undefined;

export const canPause = (item: GtmItem) => !isHeld(item) && item.status !== GtmItemStatus.SENT && item.status !== GtmItemStatus.PAUSED && item.status !== GtmItemStatus.PUBLISHED;

export const canResume = (item: GtmItem) => !isHeld(item) && item.status === GtmItemStatus.PAUSED;

/** Content (title, body, format, channel) and the date can change: anything not out and not held. */
export const canEdit = (item: GtmItem) => !isHeld(item) && !out(item);

/** A content edit here sends it back to review (it must be approved again). */
export const editReturnsToReview = (item: GtmItem) => item.status === GtmItemStatus.APPROVED || item.status === GtmItemStatus.SCHEDULED;

/** The manual channel's step applies: a non-email item on a MANUAL channel, not out yet. */
export const postsByHand = (item: GtmItem, channel: GtmChannel | undefined) => isManualChannel(channel) && !isEmailItem(item) && !out(item) && !isHeld(item);

/** It is approved, so a person who posted it by hand may record the URL and mark it PUBLISHED. */
export const canMarkPublished = (item: GtmItem, channel: GtmChannel | undefined) =>
  postsByHand(item, channel) && (item.status === GtmItemStatus.APPROVED || item.status === GtmItemStatus.SCHEDULED);
