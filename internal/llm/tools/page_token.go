// Copyright (c) 2025 Reliant Labs
package tools

import (
	"errors"
	"strconv"
	"strings"
)

// A tool that pages a list it reads whole (list_triggers, worktree list)
// hands the model the offset of the next page as its page_token. The list is
// re-read on every call, so a page is a window onto the list as it is now: an
// item added or removed between calls can shift a later page by that much,
// which a model reading a listing tolerates.

// errBadPageToken is the refusal of a token this package did not hand out.
var errBadPageToken = errors.New("page_token is not one a previous call returned; list again without page_token")

// encodeOffsetToken is the page_token of the page starting at offset.
func encodeOffsetToken(offset int) string { return strconv.Itoa(offset) }

// decodeOffsetToken is the offset a page_token names; "" is the first page.
func decodeOffsetToken(token string) (int, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(token)
	if err != nil || offset < 0 {
		return 0, errBadPageToken
	}
	return offset, nil
}
