//! Matching engine core logic.
//!
//! `match_order` is a pure function: it takes a mutable reference to the
//! [`OrderBook`] and a single [`OrderEvent`], applies price-time priority
//! matching, and returns the resulting fills and rejections.  It has no I/O,
//! no global state, and no side effects beyond the book mutation passed in.

use crate::{FillEvent, OrderBook, OrderEvent, OrderId, Price, RejectEvent, RejectReason, Side};

/// Process a single [`OrderEvent`] against the book.
///
/// Returns `(fills, rejections)`.  Does **not** touch timestamps; uses
/// `seq_num` for time priority only.
pub fn match_order(
    book: &mut OrderBook,
    event: OrderEvent,
) -> (Vec<FillEvent>, Vec<RejectEvent>) {
    match event {
        OrderEvent::LimitOrder {
            id,
            side,
            price,
            qty,
            seq_num,
        } => process_limit(book, id, side, price, qty, seq_num),

        OrderEvent::MarketOrder {
            id,
            side,
            qty,
            seq_num,
        } => process_market(book, id, side, qty, seq_num),

        OrderEvent::CancelOrder { id } => process_cancel(book, id),
    }
}

// ---------------------------------------------------------------------------
// Limit order
// ---------------------------------------------------------------------------

fn process_limit(
    book: &mut OrderBook,
    id: OrderId,
    side: Side,
    price: Price,
    qty: u64,
    seq_num: u64,
) -> (Vec<FillEvent>, Vec<RejectEvent>) {
    let mut fills = Vec::new();
    let mut remaining = qty;

    match side {
        Side::Buy => {
            // Cross against asks while best_ask <= limit price and qty remains.
            loop {
                if remaining == 0 {
                    break;
                }
                // Check whether best ask price is ≤ our limit.
                let best_ask_price = match book.best_ask() {
                    Some(p) if p <= price => p,
                    _ => break,
                };
                fill_against_ask(book, &id, side, best_ask_price, &mut remaining, &mut fills);
            }
        }
        Side::Sell => {
            // Cross against bids while best_bid >= limit price and qty remains.
            loop {
                if remaining == 0 {
                    break;
                }
                let best_bid_price = match book.best_bid() {
                    Some(p) if p >= price => p,
                    _ => break,
                };
                fill_against_bid(book, &id, side, best_bid_price, &mut remaining, &mut fills);
            }
        }
    }

    // If remainder > 0 for a limit order, rest it on the book.
    if remaining > 0 {
        let resting = crate::Order {
            id,
            side,
            price,
            qty: remaining,
            seq_num,
        };
        book.add_limit(resting);
    }

    (fills, vec![])
}

// ---------------------------------------------------------------------------
// Market order
// ---------------------------------------------------------------------------

fn process_market(
    book: &mut OrderBook,
    id: OrderId,
    side: Side,
    qty: u64,
    _seq_num: u64,
) -> (Vec<FillEvent>, Vec<RejectEvent>) {
    let mut fills = Vec::new();
    let mut rejections = Vec::new();
    let mut remaining = qty;

    match side {
        Side::Buy => {
            loop {
                if remaining == 0 {
                    break;
                }
                let best_ask_price = match book.best_ask() {
                    Some(p) => p,
                    None => break,
                };
                fill_against_ask(book, &id, side, best_ask_price, &mut remaining, &mut fills);
            }
        }
        Side::Sell => {
            loop {
                if remaining == 0 {
                    break;
                }
                let best_bid_price = match book.best_bid() {
                    Some(p) => p,
                    None => break,
                };
                fill_against_bid(book, &id, side, best_bid_price, &mut remaining, &mut fills);
            }
        }
    }

    // Market order cannot rest — if unfilled qty remains, reject it.
    if remaining > 0 {
        rejections.push(RejectEvent {
            order_id: id,
            reason: RejectReason::NoLiquidity,
        });
    }

    (fills, rejections)
}

// ---------------------------------------------------------------------------
// Cancel order
// ---------------------------------------------------------------------------

fn process_cancel(book: &mut OrderBook, id: OrderId) -> (Vec<FillEvent>, Vec<RejectEvent>) {
    if book.cancel(&id) {
        (vec![], vec![])
    } else {
        (
            vec![],
            vec![RejectEvent {
                order_id: id,
                reason: RejectReason::UnknownOrderId,
            }],
        )
    }
}

// ---------------------------------------------------------------------------
// Internal crossing helpers
// ---------------------------------------------------------------------------

/// Fill the aggressor against resting orders at `ask_price`.
/// Mutates `remaining` in place and appends to `fills`.
fn fill_against_ask(
    book: &mut OrderBook,
    aggressor_id: &OrderId,
    aggressor_side: Side,
    ask_price: Price,
    remaining: &mut u64,
    fills: &mut Vec<FillEvent>,
) {
    // Work through the FIFO queue at this price level.
    loop {
        if *remaining == 0 {
            break;
        }
        // Peek at the front of the ask queue at ask_price.
        let inv_key = ask_price.0; // asks use raw key
        let front_qty = {
            let queue = match book.asks.levels.get(&inv_key) {
                Some(q) if !q.is_empty() => q,
                _ => break,
            };
            queue.front().map(|o| o.qty).unwrap_or(0)
        };
        let fill_qty = (*remaining).min(front_qty);

        // Get resting order id before mutation.
        let resting_id = {
            let queue = book.asks.levels.get(&inv_key).unwrap();
            queue.front().unwrap().id.clone()
        };

        fills.push(FillEvent {
            aggressor_id: aggressor_id.clone(),
            resting_id: resting_id.clone(),
            price: ask_price,
            qty: fill_qty,
            aggressor_side,
        });

        *remaining -= fill_qty;

        // Update or remove the resting order.
        if fill_qty == front_qty {
            // Fully filled — remove from book and cancel_index.
            book.cancel_index.remove(&resting_id);
            let queue = book.asks.levels.get_mut(&inv_key).unwrap();
            queue.pop_front();
            if queue.is_empty() {
                book.asks.levels.remove(&inv_key);
            }
        } else {
            // Partially filled — update qty in place.
            let queue = book.asks.levels.get_mut(&inv_key).unwrap();
            queue.front_mut().unwrap().qty -= fill_qty;
            break; // We've taken all of `remaining`.
        }
    }
}

/// Fill the aggressor against resting orders at `bid_price`.
/// Mutates `remaining` in place and appends to `fills`.
fn fill_against_bid(
    book: &mut OrderBook,
    aggressor_id: &OrderId,
    aggressor_side: Side,
    bid_price: Price,
    remaining: &mut u64,
    fills: &mut Vec<FillEvent>,
) {
    let inv_key = u64::MAX - bid_price.0; // bids use inverted key
    loop {
        if *remaining == 0 {
            break;
        }
        let front_qty = {
            let queue = match book.bids.levels.get(&inv_key) {
                Some(q) if !q.is_empty() => q,
                _ => break,
            };
            queue.front().map(|o| o.qty).unwrap_or(0)
        };
        let fill_qty = (*remaining).min(front_qty);

        let resting_id = {
            let queue = book.bids.levels.get(&inv_key).unwrap();
            queue.front().unwrap().id.clone()
        };

        fills.push(FillEvent {
            aggressor_id: aggressor_id.clone(),
            resting_id: resting_id.clone(),
            price: bid_price,
            qty: fill_qty,
            aggressor_side,
        });

        *remaining -= fill_qty;

        if fill_qty == front_qty {
            book.cancel_index.remove(&resting_id);
            let queue = book.bids.levels.get_mut(&inv_key).unwrap();
            queue.pop_front();
            if queue.is_empty() {
                book.bids.levels.remove(&inv_key);
            }
        } else {
            let queue = book.bids.levels.get_mut(&inv_key).unwrap();
            queue.front_mut().unwrap().qty -= fill_qty;
            break;
        }
    }
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{Order, OrderBook, OrderEvent, OrderId, Price, RejectReason, Side};

    fn oid(s: &str) -> OrderId {
        OrderId(s.to_string())
    }

    fn limit(id: &str, side: Side, price: u64, qty: u64, seq: u64) -> OrderEvent {
        OrderEvent::LimitOrder {
            id: oid(id),
            side,
            price: Price(price),
            qty,
            seq_num: seq,
        }
    }

    fn market(id: &str, side: Side, qty: u64, seq: u64) -> OrderEvent {
        OrderEvent::MarketOrder {
            id: oid(id),
            side,
            qty,
            seq_num: seq,
        }
    }

    fn cancel(id: &str) -> OrderEvent {
        OrderEvent::CancelOrder { id: oid(id) }
    }

    fn add_resting(book: &mut OrderBook, id: &str, side: Side, price: u64, qty: u64, seq: u64) {
        book.add_limit(Order {
            id: oid(id),
            side,
            price: Price(price),
            qty,
            seq_num: seq,
        });
    }

    // -----------------------------------------------------------------------
    // Limit order crossing tests
    // -----------------------------------------------------------------------

    #[test]
    fn limit_buy_crosses_resting_ask() {
        let mut book = OrderBook::new();
        add_resting(&mut book, "ask1", Side::Sell, 100, 10, 1);

        let (fills, rejects) = match_order(&mut book, limit("buy1", Side::Buy, 100, 10, 2));

        assert_eq!(fills.len(), 1);
        assert!(rejects.is_empty());
        let f = &fills[0];
        assert_eq!(f.aggressor_id, oid("buy1"));
        assert_eq!(f.resting_id, oid("ask1"));
        assert_eq!(f.price, Price(100));
        assert_eq!(f.qty, 10);
        assert!(book.is_empty());
    }

    #[test]
    fn limit_sell_crosses_resting_bid() {
        let mut book = OrderBook::new();
        add_resting(&mut book, "bid1", Side::Buy, 100, 10, 1);

        let (fills, rejects) = match_order(&mut book, limit("sell1", Side::Sell, 100, 10, 2));

        assert_eq!(fills.len(), 1);
        assert!(rejects.is_empty());
        let f = &fills[0];
        assert_eq!(f.aggressor_id, oid("sell1"));
        assert_eq!(f.resting_id, oid("bid1"));
        assert_eq!(f.price, Price(100));
        assert_eq!(f.qty, 10);
        assert!(book.is_empty());
    }

    #[test]
    fn partial_fill_rests_remainder_on_book() {
        let mut book = OrderBook::new();
        // Ask for 5, buyer wants 10 — 5 fills, 5 rests.
        add_resting(&mut book, "ask1", Side::Sell, 100, 5, 1);

        let (fills, rejects) = match_order(&mut book, limit("buy1", Side::Buy, 100, 10, 2));

        assert_eq!(fills.len(), 1);
        assert!(rejects.is_empty());
        assert_eq!(fills[0].qty, 5);
        // The remaining 5 should be resting on the bid side.
        assert_eq!(book.bid_qty_at(Price(100)), 5);
        assert_eq!(book.best_ask(), None);
    }

    #[test]
    fn limit_buy_no_cross_when_ask_higher() {
        let mut book = OrderBook::new();
        add_resting(&mut book, "ask1", Side::Sell, 105, 10, 1);

        // Buy limit of 100 should not cross ask at 105.
        let (fills, rejects) = match_order(&mut book, limit("buy1", Side::Buy, 100, 10, 2));

        assert!(fills.is_empty());
        assert!(rejects.is_empty());
        // Both orders rest on the book.
        assert_eq!(book.bid_qty_at(Price(100)), 10);
        assert_eq!(book.ask_qty_at(Price(105)), 10);
    }

    // -----------------------------------------------------------------------
    // Market order tests
    // -----------------------------------------------------------------------

    #[test]
    fn market_order_exhausts_liquidity_reject() {
        let mut book = OrderBook::new();
        add_resting(&mut book, "ask1", Side::Sell, 100, 3, 1);

        // Market buy for 10 but only 3 available.
        let (fills, rejects) = match_order(&mut book, market("mkt1", Side::Buy, 10, 2));

        assert_eq!(fills.len(), 1);
        assert_eq!(fills[0].qty, 3);
        assert_eq!(rejects.len(), 1);
        assert_eq!(rejects[0].order_id, oid("mkt1"));
        assert_eq!(rejects[0].reason, RejectReason::NoLiquidity);
        assert!(book.is_empty());
    }

    #[test]
    fn market_order_fully_filled() {
        let mut book = OrderBook::new();
        add_resting(&mut book, "ask1", Side::Sell, 100, 10, 1);
        add_resting(&mut book, "ask2", Side::Sell, 101, 10, 2);

        let (fills, rejects) = match_order(&mut book, market("mkt1", Side::Buy, 15, 3));

        assert!(rejects.is_empty());
        // 10 from ask1 at 100, 5 from ask2 at 101.
        let total_filled: u64 = fills.iter().map(|f| f.qty).sum();
        assert_eq!(total_filled, 15);
        assert_eq!(book.ask_qty_at(Price(101)), 5);
    }

    #[test]
    fn market_order_empty_book_reject() {
        let mut book = OrderBook::new();
        let (fills, rejects) = match_order(&mut book, market("mkt1", Side::Buy, 10, 1));
        assert!(fills.is_empty());
        assert_eq!(rejects.len(), 1);
        assert_eq!(rejects[0].reason, RejectReason::NoLiquidity);
    }

    // -----------------------------------------------------------------------
    // Cancel tests
    // -----------------------------------------------------------------------

    #[test]
    fn cancel_existing_order_removed() {
        let mut book = OrderBook::new();
        add_resting(&mut book, "bid1", Side::Buy, 100, 10, 1);

        let (fills, rejects) = match_order(&mut book, cancel("bid1"));

        assert!(fills.is_empty());
        assert!(rejects.is_empty());
        assert!(book.is_empty());
    }

    #[test]
    fn cancel_unknown_order_produces_reject() {
        let mut book = OrderBook::new();
        let (fills, rejects) = match_order(&mut book, cancel("ghost"));

        assert!(fills.is_empty());
        assert_eq!(rejects.len(), 1);
        assert_eq!(rejects[0].order_id, oid("ghost"));
        assert_eq!(rejects[0].reason, RejectReason::UnknownOrderId);
    }

    // -----------------------------------------------------------------------
    // Price-time priority
    // -----------------------------------------------------------------------

    #[test]
    fn price_time_priority_earlier_order_filled_first() {
        let mut book = OrderBook::new();
        // Two asks at same price; seq 1 arrived before seq 2.
        add_resting(&mut book, "ask_early", Side::Sell, 100, 5, 1);
        add_resting(&mut book, "ask_late", Side::Sell, 100, 5, 2);

        // Buy only enough to fill the first.
        let (fills, _) = match_order(&mut book, limit("buy1", Side::Buy, 100, 5, 3));

        assert_eq!(fills.len(), 1);
        assert_eq!(fills[0].resting_id, oid("ask_early"));
    }

    #[test]
    fn price_priority_best_price_filled_before_worse_price() {
        let mut book = OrderBook::new();
        // Two asks at different prices.
        add_resting(&mut book, "ask_high", Side::Sell, 102, 5, 2);
        add_resting(&mut book, "ask_low", Side::Sell, 100, 5, 1);

        let (fills, _) = match_order(&mut book, market("buy1", Side::Buy, 5, 3));

        assert_eq!(fills.len(), 1);
        // Should fill against the lower ask (100) first.
        assert_eq!(fills[0].resting_id, oid("ask_low"));
        assert_eq!(fills[0].price, Price(100));
    }

    #[test]
    fn multi_level_fill_generates_multiple_fill_events() {
        let mut book = OrderBook::new();
        add_resting(&mut book, "a1", Side::Sell, 100, 3, 1);
        add_resting(&mut book, "a2", Side::Sell, 101, 3, 2);

        let (fills, rejects) = match_order(&mut book, limit("b1", Side::Buy, 101, 6, 3));

        assert!(rejects.is_empty());
        assert_eq!(fills.len(), 2);
        assert_eq!(fills[0].price, Price(100));
        assert_eq!(fills[1].price, Price(101));
        assert!(book.is_empty());
    }
}
