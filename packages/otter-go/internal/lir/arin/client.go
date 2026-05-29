// ARIN Reg-RWS HTTP client — submit reassign-detailed requests for
// approved LIR allocations.
//
// The Reg-RWS API takes XML payloads and an apikey URL parameter
// (no bearer header). On success, ARIN returns a NetBlock element
// with the assigned NetHandle; the worker stamps that handle on the
// allocation. On failure (4xx), ARIN returns an Error element with
// a message — the worker stores it in arin_last_error so the NIC can
// triage. 5xx and network errors are classified transient by the
// caller (worker) and retried per the backoff schedule.
//
// Endpoint base comes from the system_settings row
// `arin.regrws.endpoint`. Defaults to ARIN's OT&E (test) environment
// — operators flip a different setting to point at the production
// endpoint once they're ready.
//
// Reference: https://www.arin.net/resources/manage/regrws/methods/
//
// XML schema for reassign-detailed:
// https://www.arin.net/resources/manage/regrws/payloads/#net-payload-detailed
package arin

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// EndpointOTE is the ARIN OT&E (Operational Test & Evaluation)
// endpoint — sandboxed against the live registry. The default until
// an operator flips the system_settings row.
const EndpointOTE = "https://reg.ote.arin.net"

// EndpointProd is the production endpoint. Set
// system_settings.arin.regrws.endpoint to this string to go live.
const EndpointProd = "https://reg.arin.net"

// Config bundles everything the client needs at construction time.
// Operators rotate APIKey by upserting the system_settings row;
// callers re-Load before each tick to pick up rotations without a
// restart.
type Config struct {
	Endpoint string
	APIKey   string
	Enabled  bool
	// Timeout bounds a single ARIN call. ARIN's stated response time
	// is "near real time" but transient slowness happens; a 30s cap
	// keeps a stuck call from starving the worker loop.
	Timeout time.Duration
}

// Client wraps a stdlib http.Client with the ARIN-specific request
// + response shape. Safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient constructs a Client. Pass http=nil to use a default
// timeout-bounded client; pass a custom *http.Client (e.g. with an
// instrumented RoundTripper) to share transport state.
func NewClient(cfg Config, h *http.Client) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if h == nil {
		h = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, http: h}
}

// contentTypeXML is the MIME type Reg-RWS sends and accepts. Set on
// both the request Content-Type (POST payloads) and the response
// Accept header.
const contentTypeXML = "application/xml"

// fmtArinHTTPErr is the canonical "%w: arin http %d: %s" template
// used by both classifyResponse (submit) and classifyRemoveResponse
// (delete). Keeps the error shape consistent across directions so
// operator dashboards can parse a single format.
const fmtArinHTTPErr = "%w: arin http %d: %s"

// ErrTransient marks an error the caller should retry per the backoff
// schedule (5xx, network failure, timeout). Wrapped errors keep their
// original cause for logging.
var ErrTransient = errors.New("arin transient error")

// ErrPermanent marks an error the caller should NOT auto-retry —
// the payload or auth is wrong and a fresh attempt will fail the same
// way. Operators reset via the manual retry endpoint after fixing
// the upstream cause.
var ErrPermanent = errors.New("arin permanent error")

// SubmitResult is the outcome of a successful reassign-detailed call.
// NetHandle is the ARIN-assigned handle on the new sub-allocation —
// the worker stamps it on lir_allocations.arin_net_handle.
type SubmitResult struct {
	NetHandle string
	RawXML    string // for audit / debugging
}

// RemoveReassignment issues a DELETE on /rest/net/{net_handle} to
// dissolve a previously-reassigned sub-allocation. Returns the same
// ErrTransient / ErrPermanent error classification as the submit
// path; phase 6's worker drives this for allocations confirmed as
// returned that had previously registered with ARIN.
//
// On success the worker stamps arin_status='removed'; the existing
// arin_net_handle column stays for the audit trail. ARIN returns
// 200 OK on a successful deassignment with a (sometimes empty)
// XML body — we don't parse it beyond status code.
func (c *Client) RemoveReassignment(ctx context.Context, parentNetHandle, netHandle string) error {
	if !c.cfg.Enabled {
		return fmt.Errorf("%w: arin integration disabled", ErrPermanent)
	}
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return fmt.Errorf("%w: arin api key not configured", ErrPermanent)
	}
	if strings.TrimSpace(netHandle) == "" {
		// A 'registered' row should always carry a handle. If it
		// doesn't, that's a data bug — permanent so the operator
		// sees it instead of hammering ARIN.
		return fmt.Errorf("%w: allocation has no arin_net_handle", ErrPermanent)
	}
	endpoint := strings.TrimRight(c.cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = EndpointOTE
	}
	target := fmt.Sprintf("%s/rest/net/%s?apikey=%s",
		endpoint, url.PathEscape(netHandle), url.QueryEscape(c.cfg.APIKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrPermanent, err)
	}
	req.Header.Set("Accept", contentTypeXML)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: http: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return classifyRemoveResponse(resp.StatusCode, body)
}

// classifyRemoveResponse mirrors classifyResponse for the DELETE
// direction. We don't need to extract a handle from the body — a
// successful DELETE is just an HTTP status check.
func classifyRemoveResponse(status int, body []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status >= 500:
		return fmt.Errorf(fmtArinHTTPErr,
			ErrTransient, status, truncateBody(body))
	default:
		return fmt.Errorf(fmtArinHTTPErr,
			ErrPermanent, status, truncateBody(body))
	}
}

// SubmitReassignDetailed POSTs the payload built from `job` to
// /rest/net/{parent_net_handle}/reassign/detailed and returns the
// assigned NetHandle on success. On failure the error is wrapped in
// ErrTransient or ErrPermanent so the caller can choose retry vs
// permanent-fail.
func (c *Client) SubmitReassignDetailed(ctx context.Context, job dbq.ArinSubmitJobRow) (SubmitResult, error) {
	if !c.cfg.Enabled {
		return SubmitResult{}, fmt.Errorf("%w: arin integration disabled", ErrPermanent)
	}
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return SubmitResult{}, fmt.Errorf("%w: arin api key not configured", ErrPermanent)
	}
	endpoint := strings.TrimRight(c.cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = EndpointOTE
	}
	payload, err := buildReassignDetailedXML(job)
	if err != nil {
		// Payload-build failures are deterministic and reflect bad
		// data; surface as permanent.
		return SubmitResult{}, fmt.Errorf("%w: build payload: %v", ErrPermanent, err)
	}
	target := fmt.Sprintf("%s/rest/net/%s/reassign/detailed?apikey=%s",
		endpoint, url.PathEscape(job.ParentNetHandle), url.QueryEscape(c.cfg.APIKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return SubmitResult{}, fmt.Errorf("%w: build request: %v", ErrPermanent, err)
	}
	req.Header.Set("Content-Type", contentTypeXML)
	req.Header.Set("Accept", contentTypeXML)
	resp, err := c.http.Do(req)
	if err != nil {
		// Network / timeout / DNS — all transient.
		return SubmitResult{}, fmt.Errorf("%w: http: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return classifyResponse(resp.StatusCode, body)
}

// classifyResponse maps an ARIN response into either SubmitResult or
// an ErrTransient/ErrPermanent error. Pulled out of the HTTP call so
// the table-driven tests can exercise every status code without a
// real RoundTripper.
func classifyResponse(status int, body []byte) (SubmitResult, error) {
	switch {
	case status >= 200 && status < 300:
		handle, perr := parseNetHandle(body)
		if perr != nil {
			// 2xx without a parseable handle is unexpected — ARIN
			// may have returned a TicketedRequest (async). Treat as
			// transient so the worker retries; if it keeps happening
			// the operator sees the same body in arin_last_error.
			return SubmitResult{}, fmt.Errorf("%w: unparseable success body: %v", ErrTransient, perr)
		}
		return SubmitResult{NetHandle: handle, RawXML: string(body)}, nil
	case status >= 500:
		return SubmitResult{}, fmt.Errorf(fmtArinHTTPErr,
			ErrTransient, status, truncateBody(body))
	default:
		// 4xx: bad payload, bad handle, auth, throttling. ARIN's 429
		// is rare and we treat it as permanent here so the operator
		// sees it — switching to transient would risk burning auto-
		// retries against a misconfig.
		return SubmitResult{}, fmt.Errorf(fmtArinHTTPErr,
			ErrPermanent, status, truncateBody(body))
	}
}

// truncateBody caps an error message at 1KB so a verbose ARIN
// response doesn't blow past the arin_last_error column's 2048-char
// budget (the column would silently truncate; we'd rather see the
// useful prefix).
func truncateBody(b []byte) string {
	const cap = 1024
	if len(b) > cap {
		return string(b[:cap]) + "...[truncated]"
	}
	return string(b)
}

// ---- XML payload ----

// netDetailed is the XML root the reassign-detailed endpoint expects.
// Subset of the documented schema — we send the minimum ARIN needs
// to register the sub-allocation: range, customer Org with address +
// POCs. Optional fields (parentNetHandle, originAS, comment) are
// emitted only when set.
type netDetailed struct {
	XMLName xml.Name `xml:"net"`
	XMLNS   string   `xml:"xmlns,attr"`

	StartAddress string `xml:"startAddress"`
	EndAddress   string `xml:"endAddress"`

	// NetBlocks must contain at least one netBlock with CIDR length
	// + type=AR (reassign). ARIN uses startAddress/endAddress + the
	// netBlock to derive the allocation range.
	NetBlocks struct {
		NetBlock netBlockXML `xml:"netBlock"`
	} `xml:"netBlocks"`

	CustomerName *string `xml:"customerName,omitempty"`

	// The customer Org block carries POC + address. ARIN matches by
	// these fields when the OrgID isn't supplied.
	CustomerOrg orgXML `xml:"customerOrg"`
}

type netBlockXML struct {
	// "A" = reallocation, "S" = simple reassign, "AR" = detailed
	// reassign. Phase 5 always sends AR.
	Type        string `xml:"type"`
	Description string `xml:"description,omitempty"`
	// CIDR length, NOT prefix bits — ARIN wants the integer.
	CidrLength int `xml:"cidrLength"`
}

type orgXML struct {
	OrgName  string `xml:"orgName"`
	Address  string `xml:"streetAddress>line"`
	City     string `xml:"city"`
	State    string `xml:"iso3166-2,omitempty"`
	PostCode string `xml:"postalCode,omitempty"`
	Country  string `xml:"iso3166-1>code2"`

	// ARIN reassign-detailed requires Admin + Tech + Abuse POC content
	// on the customerOrg. We don't have ARIN-assigned POC handles for
	// tenant rows, so we embed inline POC payloads (name + email +
	// optional phone) under <pocs>. Operators with pre-existing ARIN
	// POC handles can switch to <pocLinks><pocLinkRef> by adding a
	// handle column on Organization and emitting that instead.
	POCs pocsXML `xml:"pocs"`
}

type pocsXML struct {
	Admin pocXML `xml:"admin"`
	Tech  pocXML `xml:"tech"`
	Abuse pocXML `xml:"abuse"`
}

type pocXML struct {
	Name  string `xml:"pocName"`
	Email string `xml:"pocEmail"`
	// Phone is optional in ARIN's schema; omit the element when
	// the source row leaves it null/empty so we don't ship
	// <pocPhone></pocPhone> which some ARIN validators reject.
	Phone string `xml:"pocPhone,omitempty"`
}

// netBlockNS is the XMLNS attr Reg-RWS expects on the root element.
// ARIN rejects payloads without it.
const netBlockNS = "http://www.arin.net/regrws/core/v1"

func buildReassignDetailedXML(job dbq.ArinSubmitJobRow) ([]byte, error) {
	// Derive start/end addresses + CIDR length from the prefix string.
	start, end, cidr, err := parsePrefixRange(job.Prefix)
	if err != nil {
		return nil, err
	}
	customerName := job.OrgName
	doc := netDetailed{
		XMLNS:        netBlockNS,
		StartAddress: start,
		EndAddress:   end,
		CustomerName: &customerName,
		CustomerOrg: orgXML{
			OrgName: job.OrgName,
			Address: job.AddressLine1,
			City:    job.City,
			Country: job.Country,
		},
	}
	doc.NetBlocks.NetBlock = netBlockXML{
		Type:       "AR",
		CidrLength: cidr,
	}
	if job.StateProvince != nil {
		doc.CustomerOrg.State = *job.StateProvince
	}
	if job.PostalCode != nil {
		doc.CustomerOrg.PostCode = *job.PostalCode
	}
	// Populate the three required POCs from the joined Organization
	// fields. ClaimNextArinSubmitJob already SELECTs them; before this
	// patch they were silently dropped, and ARIN rejected the payload
	// 4xx 'Required POC references missing' for every submission.
	doc.CustomerOrg.POCs = pocsXML{
		Admin: pocXML{Name: job.AdminPocName, Email: job.AdminPocEmail, Phone: strDeref(job.AdminPocPhone)},
		Tech:  pocXML{Name: job.TechPocName, Email: job.TechPocEmail, Phone: strDeref(job.TechPocPhone)},
		Abuse: pocXML{Name: job.AbusePocName, Email: job.AbusePocEmail, Phone: strDeref(job.AbusePocPhone)},
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	// Prepend the XML declaration; xml.Marshal omits it.
	return []byte(xml.Header + string(out)), nil
}

// ---- XML response parsing ----

// netResponse is the success-shape root. ARIN returns the assigned
// NetHandle on the assigned NetBlock inside a <net> envelope.
type netResponse struct {
	XMLName   xml.Name `xml:"net"`
	NetHandle string   `xml:"handle"`
}

func parseNetHandle(body []byte) (string, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "", errors.New("empty response body")
	}
	var nr netResponse
	if err := xml.Unmarshal(body, &nr); err != nil {
		return "", fmt.Errorf("parse net handle: %w", err)
	}
	if strings.TrimSpace(nr.NetHandle) == "" {
		return "", errors.New("response missing net handle")
	}
	return nr.NetHandle, nil
}

// parsePrefixRange turns "10.0.0.0/24" into the start address, end
// address, and CIDR length the Reg-RWS payload wants. v6 is handled
// the same way; ARIN accepts colon-separated addresses verbatim.
func parsePrefixRange(prefix string) (string, string, int, error) {
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return "", "", 0, fmt.Errorf("not a cidr: %q", prefix)
	}
	start := p.Masked().Addr()
	end, err := broadcastAddr(p)
	if err != nil {
		return "", "", 0, err
	}
	return start.String(), end.String(), p.Bits(), nil
}

// strDeref returns the pointed-at string, or "" when the pointer is
// nil. Used to flatten *string POC-phone fields into the pocXML
// struct, which uses omitempty to suppress empty elements.
func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// broadcastAddr returns the highest address in the prefix's range —
// network OR host-mask. Used to build the ARIN payload's endAddress.
// Works for both v4 and v6 by operating on the raw address bytes.
func broadcastAddr(p netip.Prefix) (netip.Addr, error) {
	masked := p.Masked()
	addr := masked.Addr()
	b := addr.AsSlice()
	totalBits := addr.BitLen()
	hostBits := totalBits - masked.Bits()
	if hostBits < 0 {
		return netip.Addr{}, fmt.Errorf("prefix size %d > family bits %d", masked.Bits(), totalBits)
	}
	// OR the host bits into b, working from the rightmost bit upward.
	for i := 0; i < hostBits; i++ {
		byteIdx := len(b) - 1 - i/8
		bitInByte := i % 8
		b[byteIdx] |= 1 << bitInByte
	}
	end, ok := netip.AddrFromSlice(b)
	if !ok {
		return netip.Addr{}, errors.New("broadcast: rebuild addr failed")
	}
	return end, nil
}
