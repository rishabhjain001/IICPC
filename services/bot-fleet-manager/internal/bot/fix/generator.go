// Package fix implements a FIX 4.2 message generator for Synthetic Trading Bots.
// Messages are built as raw bytes without any external FIX library.
//
// References: Requirements 6.1, 6.4, 6.7
package fix

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// FIX tag constants
	tagBeginString  = 8
	tagBodyLength   = 9
	tagMsgType      = 35
	tagMsgSeqNum    = 34
	tagSendingTime  = 52
	tagClOrdID      = 11
	tagOrigClOrdID  = 41
	tagSymbol       = 55
	tagSide         = 54
	tagOrderQty     = 38
	tagPrice        = 44
	tagOrdType      = 40
	tagTimeInForce  = 59
	tagChecksum     = 10

	soh = "\x01" // FIX field delimiter (SOH = 0x01)
)

// FIXGenerator generates FIX 4.2 messages for a specific bot session.
// seqNum is atomic and starts at 1; it strictly increases per session.
type FIXGenerator struct {
	BenchmarkRunID string
	BotID          string
	seqNum         uint64 // atomic counter; starts at 0, incremented before use → first value is 1
}

// FIXMessage holds a generated FIX 4.2 message and its metadata.
type FIXMessage struct {
	Raw        []byte // raw FIX 4.2 bytes
	ClOrdID    string
	SeqNum     uint64
	SendTimeNs int64
}

// nextSeqNum atomically increments and returns the next sequence number (1-based).
func (g *FIXGenerator) nextSeqNum() uint64 {
	return atomic.AddUint64(&g.seqNum, 1)
}

// clOrdID builds a globally-unique ClOrdID for this benchmark run/bot/sequence.
func (g *FIXGenerator) clOrdID(seq uint64) string {
	return fmt.Sprintf("%s-%s-%d", g.BenchmarkRunID, g.BotID, seq)
}

// buildMessage assembles raw FIX 4.2 bytes from a msgType and body tags.
// Layout:
//
//	8=FIX.4.2\x01 9=<bodyLen>\x01 35=<msgType>\x01 34=<seqNum>\x01 52=<sendingTimeNs>\x01
//	11=<clOrdID>\x01 ...bodyTags... 10=<checksum>\x01
func buildMessage(msgType string, seqNum uint64, sendTimeNs int64, clOrdID string, bodyTags string) []byte {
	// Assemble the body: everything between (and including) tag 35 up to (excluding) tag 10.
	// Per FIX spec the body length covers from tag 35 to the delimiter before tag 10.
	body := fmt.Sprintf(
		"%d=%s%s%d=%d%s%d=%d%s%d=%s%s%s",
		tagMsgType, msgType, soh,
		tagMsgSeqNum, seqNum, soh,
		tagSendingTime, sendTimeNs, soh,
		tagClOrdID, clOrdID, soh,
		bodyTags,
	)
	bodyLen := len(body)

	header := fmt.Sprintf("%d=FIX.4.2%s%d=%d%s", tagBeginString, soh, tagBodyLength, bodyLen, soh)

	// Compute checksum: sum of all bytes in header + body, mod 256.
	sum := 0
	for _, b := range []byte(header) {
		sum += int(b)
	}
	for _, b := range []byte(body) {
		sum += int(b)
	}
	checksum := sum % 256

	raw := fmt.Sprintf("%s%s%d=%03d%s", header, body, tagChecksum, checksum, soh)
	return []byte(raw)
}

// LimitOrder generates a FIX 4.2 New Order Single (MsgType=D) for a limit order.
// side: "1" = Buy, "2" = Sell
// TimeInForce 0 = Day order.
func (g *FIXGenerator) LimitOrder(side string, qty int64, price float64) FIXMessage {
	seq := g.nextSeqNum()
	now := time.Now().UnixNano()
	cid := g.clOrdID(seq)

	// body tags after the fixed header portion (55, 54, 38, 44, 40, 59)
	bodyTags := fmt.Sprintf(
		"%d=AAPL%s%d=%s%s%d=%d%s%d=%g%s%d=2%s%d=0%s",
		tagSymbol, soh,
		tagSide, side, soh,
		tagOrderQty, qty, soh,
		tagPrice, price, soh,
		tagOrdType, soh, // 2 = Limit
		tagTimeInForce, soh, // 0 = Day
	)

	raw := buildMessage("D", seq, now, cid, bodyTags)
	return FIXMessage{Raw: raw, ClOrdID: cid, SeqNum: seq, SendTimeNs: now}
}

// MarketOrder generates a FIX 4.2 New Order Single (MsgType=D) for a market order.
// side: "1" = Buy, "2" = Sell
func (g *FIXGenerator) MarketOrder(side string, qty int64) FIXMessage {
	seq := g.nextSeqNum()
	now := time.Now().UnixNano()
	cid := g.clOrdID(seq)

	bodyTags := fmt.Sprintf(
		"%d=AAPL%s%d=%s%s%d=%d%s%d=1%s",
		tagSymbol, soh,
		tagSide, side, soh,
		tagOrderQty, qty, soh,
		tagOrdType, soh, // 1 = Market
	)

	raw := buildMessage("D", seq, now, cid, bodyTags)
	return FIXMessage{Raw: raw, ClOrdID: cid, SeqNum: seq, SendTimeNs: now}
}

// CancelReplace generates a FIX 4.2 Order Cancel/Replace Request (MsgType=G).
func (g *FIXGenerator) CancelReplace(origClOrdID string, side string, qty int64, price float64) FIXMessage {
	seq := g.nextSeqNum()
	now := time.Now().UnixNano()
	cid := g.clOrdID(seq)

	bodyTags := fmt.Sprintf(
		"%d=%s%s%d=AAPL%s%d=%s%s%d=%d%s%d=%g%s",
		tagOrigClOrdID, origClOrdID, soh,
		tagSymbol, soh,
		tagSide, side, soh,
		tagOrderQty, qty, soh,
		tagPrice, price, soh,
	)

	raw := buildMessage("G", seq, now, cid, bodyTags)
	return FIXMessage{Raw: raw, ClOrdID: cid, SeqNum: seq, SendTimeNs: now}
}

// verifyChecksum recomputes and checks the FIX checksum embedded in a raw message.
// Returns (expected, ok).
func verifyChecksum(raw []byte) (int, bool) {
	s := string(raw)
	// tag 10 trailer format: ...10=NNN\x01
	idx := strings.LastIndex(s, soh+"10=")
	if idx < 0 {
		// Maybe it starts at the very beginning (no prior SOH)
		if strings.HasPrefix(s, "10=") {
			idx = -1
		} else {
			return -1, false
		}
	}
	// sum bytes up to and including the SOH before tag 10
	payloadEnd := idx + 1 // include the SOH that precedes "10="
	sum := 0
	for _, b := range raw[:payloadEnd] {
		sum += int(b)
	}
	expected := sum % 256

	// parse the embedded checksum
	checksumStart := idx + len(soh+"10=")
	if checksumStart+3 > len(s) {
		return expected, false
	}
	var embedded int
	_, err := fmt.Sscanf(s[checksumStart:checksumStart+3], "%d", &embedded)
	if err != nil {
		return expected, false
	}
	return expected, expected == embedded
}
