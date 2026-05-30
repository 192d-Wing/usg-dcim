package httpx

import (
	"net/url"
	"testing"
)

func TestPageBounds_Defaults(t *testing.T) {
	limit, offset := PageBounds(url.Values{})
	if limit != 50 {
		t.Errorf("default limit = %d, want 50", limit)
	}
	if offset != 0 {
		t.Errorf("default offset = %d, want 0", offset)
	}
}

func TestPageBounds_LimitWinsOverPageSize(t *testing.T) {
	q := url.Values{"limit": {"25"}, "page_size": {"99"}}
	limit, _ := PageBounds(q)
	if limit != 25 {
		t.Errorf("?limit should beat ?page_size; got %d", limit)
	}
}

func TestPageBounds_PageSizeWhenNoLimit(t *testing.T) {
	q := url.Values{"page_size": {"75"}}
	limit, _ := PageBounds(q)
	if limit != 75 {
		t.Errorf("?page_size should be honored when no ?limit; got %d", limit)
	}
}

func TestPageBounds_OffsetWinsOverPage(t *testing.T) {
	q := url.Values{"page": {"3"}, "page_size": {"20"}, "offset": {"5"}}
	_, offset := PageBounds(q)
	if offset != 5 {
		t.Errorf("explicit ?offset should win over ?page; got %d", offset)
	}
}

func TestPageBounds_PageComputesOffset(t *testing.T) {
	q := url.Values{"page": {"3"}, "page_size": {"20"}}
	limit, offset := PageBounds(q)
	if limit != 20 {
		t.Errorf("limit = %d, want 20", limit)
	}
	if offset != 40 {
		t.Errorf("offset = %d, want 40 (page=3, page_size=20)", offset)
	}
}

func TestPageBounds_PageOneIsOffsetZero(t *testing.T) {
	q := url.Values{"page": {"1"}, "page_size": {"50"}}
	_, offset := PageBounds(q)
	if offset != 0 {
		t.Errorf("page=1 → offset=0; got %d", offset)
	}
}

// page=0 is invalid (1-indexed); parseBoundedInt32's lo=1 floors it
// to page=1, which is offset=0. Without the floor we'd compute
// (0-1)*limit = -limit and the SQL OFFSET would underflow.
func TestPageBounds_PageZeroFloorsToOne(t *testing.T) {
	q := url.Values{"page": {"0"}, "page_size": {"50"}}
	_, offset := PageBounds(q)
	if offset != 0 {
		t.Errorf("page=0 should floor to page=1 (offset=0); got %d", offset)
	}
}

func TestPageBounds_NegativeOffsetClamped(t *testing.T) {
	q := url.Values{"offset": {"-10"}}
	_, offset := PageBounds(q)
	if offset != 0 {
		t.Errorf("negative offset clamped to 0; got %d", offset)
	}
}

// Bound: page * limit must not overflow int32. The 1M cap on page and
// 500 cap on limit yield a max product of 500M, well under 2.1B.
func TestPageBounds_HugePageClamped(t *testing.T) {
	q := url.Values{"page": {"99999999"}, "page_size": {"500"}}
	limit, offset := PageBounds(q)
	if limit != 500 {
		t.Errorf("limit clamped; got %d", limit)
	}
	// page clamped to 1_000_000, offset = (1_000_000-1)*500 = 499_999_500
	if offset != 499_999_500 {
		t.Errorf("offset = %d, want 499_999_500", offset)
	}
}

func TestPageBounds_LimitClampedToCeiling(t *testing.T) {
	q := url.Values{"limit": {"99999"}}
	limit, _ := PageBounds(q)
	if limit != 500 {
		t.Errorf("limit clamped to 500; got %d", limit)
	}
}

func TestPageBounds_NonNumericFallsBackToDefault(t *testing.T) {
	q := url.Values{"limit": {"banana"}, "offset": {"squash"}, "page": {"melon"}}
	limit, offset := PageBounds(q)
	if limit != 50 || offset != 0 {
		t.Errorf("non-numeric → defaults; got limit=%d offset=%d", limit, offset)
	}
}
