// Copyright (c) 2026 Reliant Labs

/**
 * What a PERSON may do to an item, by its status: the lifecycle on
 * GtmItemStatus (proto/reliant/v1/gtm.proto), which the service enforces and
 * these mirror so the pages offer only what it would accept.
 *
 *   - DRAFT or REVIEW → APPROVED;
 *   - APPROVED → SCHEDULED, for an item that is not an EMAIL and has a date;
 *   - any status but SENT → PAUSED, and PAUSED → DRAFT;
 *   - a content edit of an APPROVED or SCHEDULED item returns it to REVIEW;
 *   - SENT is the email send path's alone, and a SENT item is read-only.
 *
 * An EMAIL never uses SCHEDULED or PUBLISHED. Nothing here publishes or
 * sends: PUBLISHED ("it went out") is not offered at all.
 */

import { GtmItemKind, GtmItemStatus, type GtmItem } from "@/gen/reliant/v1/gtm_pb";

const isEmail = (item: GtmItem) => item.kind === GtmItemKind.EMAIL;

export const canApprove = (item: GtmItem) => item.status === GtmItemStatus.DRAFT || item.status === GtmItemStatus.REVIEW;

export const canSchedule = (item: GtmItem) => !isEmail(item) && item.status === GtmItemStatus.APPROVED && item.scheduledAt !== undefined;

export const canPause = (item: GtmItem) => item.status !== GtmItemStatus.SENT && item.status !== GtmItemStatus.PAUSED && item.status !== GtmItemStatus.PUBLISHED;

export const canResume = (item: GtmItem) => item.status === GtmItemStatus.PAUSED;

/** Content (title, body) and the date can change: anything not yet out. */
export const canEdit = (item: GtmItem) => item.status !== GtmItemStatus.SENT && item.status !== GtmItemStatus.PUBLISHED;

/** A content edit here sends it back to review (it must be approved again). */
export const editReturnsToReview = (item: GtmItem) => item.status === GtmItemStatus.APPROVED || item.status === GtmItemStatus.SCHEDULED;
