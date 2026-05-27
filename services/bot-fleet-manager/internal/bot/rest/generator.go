// Package rest implements a REST order management message generator for Synthetic Trading Bots.
//
// References: Requirements 6.2, 6.4, 6.7
package rest

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// RESTGenerator generates REST order management messages for a specific bot session.
// seqNum is atomic and starts at 1.
type RESTGenerator struct {
	BenchmarkRunID string
	BotID          string
	seqNum         uint64 // atomic counter; starts at 0, first value returned is 1
}

// RESTMessage holds a generated REST request and its metadata.
type RESTMessage struct {
	Method     string // "POST" | "DELETE"
	Path       string
	Body       []byte // JSON encoded body (empty for DELETE)
	RequestID  string
	ClOrdID    string
	SeqNum     uint64
	SendTimeNs int64
}

// orderBody is the JSON schema for a place-order request.
type orderBody struct {
	RequestID  string  `json:"request_id"`
	ClOrdID    string  `json:"cl_ord_id"`
	SeqNum     uint64  `json:"seq_num"`
	SendTimeNs int64   `json:"send_time_ns"`
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	OrdType    string  `json:"ord_type"`
	Qty        int64   `json:"qty"`
	Price      float64 `json:"price,omitempty"`
}

// nextSeqNum atomically increments and returns the next sequence number (1-based).
func (g *RESTGenerator) nextSeqNum() uint64 {
	return atomic.AddUint64(&g.seqNum, 1)
}

// newRequestID generates a random UUID v4 string using crypto/rand.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	// Set version 4 and variant bits per RFC 4122.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// clOrdID builds a globally-unique ClOrdID for this benchmark run/bot/sequence.
func (g *RESTGenerator) clOrdID(seq uint64) string {
	return fmt.Sprintf("%s-%s-%d", g.BenchmarkRunID, g.BotID, seq)
}

// PlaceOrder generates a POST /v1/orders REST message for a limit or market order.
// side: "BUY" | "SELL"
// ordType: "LIMIT" | "MARKET"
// price is ignored for MARKET orders (zero is acceptable and will be omitted via omitempty).
func (g *RESTGenerator) PlaceOrder(side string, ordType string, qty int64, price float64) RESTMessage {
	seq := g.nextSeqNum()
	now := time.Now().UnixNano()
	cid := g.clOrdID(seq)
	rid := newRequestID()

	body := orderBody{
		RequestID:  rid,
		ClOrdID:    cid,
		SeqNum:     seq,
		SendTimeNs: now,
		Symbol:     "AAPL",
		Side:       side,
		OrdType:    ordType,
		Qty:        qty,
		Price:      price,
	}

	raw, _ := json.Marshal(body)
	return RESTMessage{
		Method:     "POST",
		Path:       "/v1/orders",
		Body:       raw,
		RequestID:  rid,
		ClOrdID:    cid,
		SeqNum:     seq,
		SendTimeNs: now,
	}
}

// CancelOrder generates a DELETE /v1/orders/{clOrdID} REST message.
func (g *RESTGenerator) CancelOrder(clOrdID string) RESTMessage {
	seq := g.nextSeqNum()
	now := time.Now().UnixNano()
	rid := newRequestID()

	return RESTMessage{
		Method:     "DELETE",
		Path:       "/v1/orders/" + clOrdID,
		Body:       nil,
		RequestID:  rid,
		ClOrdID:    clOrdID,
		SeqNum:     seq,
		SendTimeNs: now,
	}
}
