// Package dnstap is the Go port of collector/src/dcim_collector/dnstap.py.
// CoreDNS streams every query handled by an auth pod to a UNIX socket
// via its `dnstap` plugin; this package listens on the socket, walks
// fstrm's bidirectional handshake, and decodes each data frame just
// far enough to pull out the DNS question name + type. The decoded
// (name, type) tuple is fed to a caller-supplied OnQuery callback,
// which the metrics loop uses to fold queries into a per-server
// top-K reservoir.
//
// Why hand-rolled rather than using github.com/dnstap/golang-dnstap:
//   - The upstream lib pulls in golang/protobuf v1 + four indirect
//     deps for one wire message. We need ~250 lines of fstrm framing
//     + ~60 lines of manual protobuf decoding to skip the dep entirely.
//   - Parity with the Python collector's hand-rolled approach keeps
//     the two implementations easy to reason about side-by-side.
//
// Bidirectional fstrm handshake (CoreDNS connects to us as the client):
//
//	reader: <accept>                  writer: <connect>
//	reader: <read READY>             writer: <send READY>
//	reader: <send ACCEPT(ctype)>     writer: <read ACCEPT>
//	reader: <read START>             writer: <send START(ctype)>
//	     --- data frames flow ---
//	reader: <read STOP>              writer: <send STOP>
//	reader: <send FINISH>            writer: <read FINISH>
package dnstap

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OnQuery is invoked for every decoded DNS query. Implementations must
// be cheap and non-blocking (or hand off to a worker); the dnstap loop
// runs single-threaded per connection.
type OnQuery func(name, qtype string)

// Frame Streams control-frame types per draft-ietf-dnsop-dnstap-09 §3.
const (
	ctlAccept = 0x01
	ctlStart  = 0x02
	ctlStop   = 0x03
	ctlReady  = 0x04
	ctlFinish = 0x05

	ctlFieldContentType = 0x01
)

var dnstapContentType = []byte("protobuf:dnstap.Dnstap")

// RFC 1035 question TYPE values + the few DNSSEC + service types we
// want pretty labels for. Anything else falls through to the numeric
// string so the top-K never drops a sample for lack of a label.
var rrTypes = map[uint16]string{
	1: "A", 2: "NS", 5: "CNAME", 6: "SOA", 12: "PTR", 15: "MX",
	16: "TXT", 24: "SIG", 28: "AAAA", 33: "SRV", 35: "NAPTR", 41: "OPT",
	43: "DS", 46: "RRSIG", 47: "NSEC", 48: "DNSKEY", 50: "NSEC3",
	51: "NSEC3PARAM", 52: "TLSA", 65: "HTTPS", 257: "CAA",
}

// Serve listens on socketPath until ctx is cancelled. CoreDNS connects
// to us; each connection is handled in its own goroutine. A stale
// socket file is removed before bind — important after a kill -9.
func Serve(ctx context.Context, socketPath string, onQuery OnQuery, log *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("mkdir socket parent: %w", err)
	}
	_ = os.Remove(socketPath)
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", socketPath, err)
	}
	// Loosen perms so CoreDNS (which may run as a non-root UID) can
	// connect; the shared volume is the security boundary.
	_ = os.Chmod(socketPath, 0o660)

	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()

	log.Info("dnstap_server_start", "socket", socketPath)
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go handleClient(conn, onQuery, log)
	}
}

func handleClient(conn net.Conn, onQuery OnQuery, log *slog.Logger) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 65536)

	// 1. Read READY from CoreDNS.
	if !readControl(br, ctlReady) {
		return
	}
	// 2. Send ACCEPT with our advertised content type.
	if _, err := conn.Write(makeAcceptFrame()); err != nil {
		return
	}
	// 3. Read START from CoreDNS, then begin draining data frames.
	if !readControl(br, ctlStart) {
		return
	}

	for {
		kind, payload, err := readFrame(br)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				log.Debug("dnstap_read_failed", "err", err)
			}
			return
		}
		if kind == frameControl {
			if len(payload) < 4 {
				continue
			}
			ctl := binary.BigEndian.Uint32(payload[:4])
			if ctl == ctlStop {
				_, _ = conn.Write(makeFinishFrame())
				return
			}
			// Spec allows future control frames mid-stream — ignore.
			continue
		}
		// Data frame: dnstap-encoded message.
		dispatch(payload, onQuery)
	}
}

// frame-kind sentinels make the read loop self-documenting.
type frameKind int

const (
	frameControl frameKind = iota
	frameData
)

// readFrame decodes one fstrm frame. Layout:
//
//	[4-byte BE length]
//	  if length == 0: escape — followed by [4-byte ctrl length][ctrl payload]
//	  else:           [length bytes of data payload]
func readFrame(r io.Reader) (frameKind, []byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length == 0 {
		// control-frame escape
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return 0, nil, err
		}
		ctl := binary.BigEndian.Uint32(lenBuf[:])
		buf := make([]byte, ctl)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, nil, err
		}
		return frameControl, buf, nil
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return frameData, buf, nil
}

// readControl pumps frames until a control frame matching `want`
// arrives. Returns false on EOF or mismatched payload.
func readControl(r io.Reader, want uint32) bool {
	kind, payload, err := readFrame(r)
	if err != nil || kind != frameControl || len(payload) < 4 {
		return false
	}
	return binary.BigEndian.Uint32(payload[:4]) == want
}

func makeControlFrame(payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	// escape prefix
	binary.BigEndian.PutUint32(out[0:4], 0)
	binary.BigEndian.PutUint32(out[4:8], uint32(len(payload)))
	copy(out[8:], payload)
	return out
}

func makeAcceptFrame() []byte {
	field := make([]byte, 8+len(dnstapContentType))
	binary.BigEndian.PutUint32(field[0:4], ctlFieldContentType)
	binary.BigEndian.PutUint32(field[4:8], uint32(len(dnstapContentType)))
	copy(field[8:], dnstapContentType)

	payload := make([]byte, 4+len(field))
	binary.BigEndian.PutUint32(payload[0:4], ctlAccept)
	copy(payload[4:], field)
	return makeControlFrame(payload)
}

func makeFinishFrame() []byte {
	var p [4]byte
	binary.BigEndian.PutUint32(p[:], ctlFinish)
	return makeControlFrame(p[:])
}

// dispatch decodes one dnstap data frame and invokes onQuery if the
// frame contains a question section we could read. Decode failures
// drop silently — one torn frame shouldn't stop the dnstap loop.
func dispatch(frame []byte, onQuery OnQuery) {
	inner := findLengthDelimited(frame, 14) // Dnstap.message
	if inner == nil {
		return
	}
	wire := findLengthDelimited(inner, 10) // Message.query_message
	if wire == nil {
		return
	}
	name, qtype, ok := decodeDNSQuestion(wire)
	if !ok {
		return
	}
	onQuery(name, qtype)
}

// --- minimal protobuf decoder -----------------------------------------------

func readVarint(b []byte, pos int) (uint64, int, error) {
	var result uint64
	var shift uint
	for pos < len(b) {
		bt := b[pos]
		result |= uint64(bt&0x7F) << shift
		pos++
		if bt&0x80 == 0 {
			return result, pos, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, pos, errors.New("varint too long")
		}
	}
	return 0, pos, errors.New("varint truncated")
}

func skipField(b []byte, pos int, wireType uint64) (int, error) {
	switch wireType {
	case 0:
		_, p, err := readVarint(b, pos)
		return p, err
	case 1:
		return pos + 8, nil
	case 2:
		length, p, err := readVarint(b, pos)
		if err != nil {
			return pos, err
		}
		return p + int(length), nil
	case 5:
		return pos + 4, nil
	default:
		return pos, fmt.Errorf("unknown wire type %d", wireType)
	}
}

// findLengthDelimited walks a protobuf message and returns the inner
// bytes of the first length-delimited (wire-type 2) field matching
// targetField. Used twice per dnstap frame.
func findLengthDelimited(data []byte, targetField int) []byte {
	pos := 0
	for pos < len(data) {
		tag, np, err := readVarint(data, pos)
		if err != nil {
			return nil
		}
		pos = np
		fieldNum := int(tag >> 3)
		wireType := tag & 0x07
		if fieldNum == targetField && wireType == 2 {
			length, p, err := readVarint(data, pos)
			if err != nil {
				return nil
			}
			pos = p
			if pos+int(length) > len(data) {
				return nil
			}
			return data[pos : pos+int(length)]
		}
		newPos, err := skipField(data, pos, wireType)
		if err != nil || newPos <= pos {
			return nil
		}
		pos = newPos
	}
	return nil
}

// --- DNS wire decoder --------------------------------------------------------

// decodeDNSQuestion parses just the question section of a DNS message.
// Returns (qname, qtype_label, ok). RFC 1035 §4.1.4 forbids compression
// pointers in the question section, so we treat any 0xC0+ length byte
// as malformed.
func decodeDNSQuestion(wire []byte) (string, string, bool) {
	if len(wire) < 12 {
		return "", "", false
	}
	pos := 12 // skip DNS header
	var labels []string
	for pos < len(wire) {
		length := int(wire[pos])
		pos++
		if length == 0 {
			break
		}
		if length&0xC0 != 0 {
			return "", "", false
		}
		if pos+length > len(wire) {
			return "", "", false
		}
		labels = append(labels, string(wire[pos:pos+length]))
		pos += length
	}
	if pos+4 > len(wire) {
		return "", "", false
	}
	qtype := binary.BigEndian.Uint16(wire[pos : pos+2])
	name := "."
	if len(labels) > 0 {
		name = strings.ToLower(strings.Join(labels, ".") + ".")
	}
	label, ok := rrTypes[qtype]
	if !ok {
		label = strconv.Itoa(int(qtype))
	}
	return name, label, true
}
