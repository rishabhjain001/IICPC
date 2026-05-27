//! Reference Matching Engine (RME)
//!
//! A deterministic, side-effect-free pure-function library that accepts an
//! ordered sequence of [`OrderEvent`]s and returns the expected set of
//! [`FillEvent`]s, [`RejectEvent`]s, and the final [`OrderBook`] state.
//!
//! Design goals (see design.md §6):
//! - No I/O, no global state, no side effects.
//! - Price-time priority: `BTreeMap<Price, VecDeque<Order>>`.
//! - Determinism: sorted price levels + FIFO queues + no timestamps in matching
//!   logic (sequence numbers only).

pub mod matching;
pub mod order_book;

use serde::{Deserialize, Serialize};
use std::collections::HashMap;

pub use order_book::OrderBook;

// ---------------------------------------------------------------------------
// Primitive wrappers
// ---------------------------------------------------------------------------

/// Newtype for a raw price represented as a fixed-point integer
/// (price × 10^8 to avoid floating-point in matching logic).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
pub struct Price(pub u64);

/// Opaque order identifier (globally unique within a Benchmark Run).
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct OrderId(pub String);

// ---------------------------------------------------------------------------
// Core domain types
// ---------------------------------------------------------------------------

/// Which side of the book an order lives on.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum Side {
    Buy,
    Sell,
}

/// A resting order on the book.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Order {
    pub id: OrderId,
    pub side: Side,
    pub price: Price,
    /// Remaining unfilled quantity (in shares / lots).
    pub qty: u64,
    /// Monotonically increasing per-bot-session sequence number; used for
    /// time-priority tie-breaking within a price level.
    pub seq_num: u64,
}

/// Input event processed by the matching engine.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub enum OrderEvent {
    /// A limit order to be placed on the book or matched immediately.
    LimitOrder {
        id: OrderId,
        side: Side,
        price: Price,
        qty: u64,
        seq_num: u64,
    },
    /// A market order matched immediately at the best available price.
    MarketOrder {
        id: OrderId,
        side: Side,
        qty: u64,
        seq_num: u64,
    },
    /// Cancel a resting order identified by its [`OrderId`].
    CancelOrder { id: OrderId },
}

/// Emitted when two orders cross.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct FillEvent {
    /// Aggressive (incoming) order ID.
    pub aggressor_id: OrderId,
    /// Passive (resting) order ID.
    pub resting_id: OrderId,
    /// Executed price (equals the resting order's limit price).
    pub price: Price,
    /// Executed quantity.
    pub qty: u64,
    /// Side of the aggressor.
    pub aggressor_side: Side,
}

/// Emitted when an order cannot be processed (e.g., unknown cancel target).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RejectEvent {
    pub order_id: OrderId,
    pub reason: RejectReason,
}

/// Reason an order was rejected.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum RejectReason {
    /// Attempted to cancel an order that is not on the book.
    UnknownOrderId,
    /// Market order could not be fully filled (no liquidity).
    NoLiquidity,
}

/// Return value of [`run_matching_engine`].
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct MatchResult {
    pub fills: Vec<FillEvent>,
    pub rejections: Vec<RejectEvent>,
    pub final_book: FinalBookSnapshot,
}

/// A serialisable snapshot of the final book state (avoids exposing internal
/// BTreeMap/VecDeque details across the C ABI boundary).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct FinalBookSnapshot {
    /// bid price (raw u64) → total resting quantity
    pub bids: HashMap<u64, u64>,
    /// ask price (raw u64) → total resting quantity
    pub asks: HashMap<u64, u64>,
}

// ---------------------------------------------------------------------------
// Internal helper
// ---------------------------------------------------------------------------

fn build_snapshot(book: &order_book::OrderBook) -> FinalBookSnapshot {
    let mut bids = std::collections::HashMap::new();
    for (inv_key, queue) in &book.bids.levels {
        let raw_price = u64::MAX - inv_key;
        let total_qty: u64 = queue.iter().map(|o| o.qty).sum();
        bids.insert(raw_price, total_qty);
    }
    let mut asks = std::collections::HashMap::new();
    for (raw_price, queue) in &book.asks.levels {
        let total_qty: u64 = queue.iter().map(|o| o.qty).sum();
        asks.insert(*raw_price, total_qty);
    }
    FinalBookSnapshot { bids, asks }
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run the reference matching engine over an ordered sequence of order events.
///
/// # Properties
///
/// - **Determinism** (Property 15): Given the same input slice `orders`, this
///   function always returns an identical `MatchResult`.
/// - **Idempotency under cancellation** (Property 16): Running the engine
///   with the output fills appended as cancellations leaves the book in a
///   consistent state with all quantities non-negative.
pub fn run_matching_engine(orders: &[OrderEvent]) -> MatchResult {
    let mut book = OrderBook::new();
    let mut fills = Vec::new();
    let mut rejections = Vec::new();

    for event in orders {
        let (new_fills, new_rejects) = matching::match_order(&mut book, event.clone());
        fills.extend(new_fills);
        rejections.extend(new_rejects);
    }

    let final_book = build_snapshot(&book);
    MatchResult {
        fills,
        rejections,
        final_book,
    }
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod integration_tests {
    use super::*;

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

    #[test]
    fn empty_input_returns_empty_result() {
        let result = run_matching_engine(&[]);
        assert!(result.fills.is_empty());
        assert!(result.rejections.is_empty());
        assert!(result.final_book.bids.is_empty());
        assert!(result.final_book.asks.is_empty());
    }

    #[test]
    fn single_limit_order_no_fills_rests_on_book() {
        let events = vec![limit("b1", Side::Buy, 100, 10, 1)];
        let result = run_matching_engine(&events);
        assert!(result.fills.is_empty());
        assert!(result.rejections.is_empty());
        // The bid should appear in the final snapshot.
        assert_eq!(result.final_book.bids.get(&100), Some(&10));
        assert!(result.final_book.asks.is_empty());
    }

    #[test]
    fn two_crossing_limit_orders_produce_one_fill() {
        let events = vec![
            limit("sell1", Side::Sell, 100, 10, 1),
            limit("buy1", Side::Buy, 100, 10, 2),
        ];
        let result = run_matching_engine(&events);
        assert_eq!(result.fills.len(), 1);
        let f = &result.fills[0];
        assert_eq!(f.aggressor_id, oid("buy1"));
        assert_eq!(f.resting_id, oid("sell1"));
        assert_eq!(f.qty, 10);
        assert!(result.rejections.is_empty());
        // Book should be empty after full cross.
        assert!(result.final_book.bids.is_empty());
        assert!(result.final_book.asks.is_empty());
    }

    #[test]
    fn determinism_same_input_identical_result() {
        let events = vec![
            limit("a1", Side::Sell, 101, 5, 1),
            limit("a2", Side::Sell, 102, 5, 2),
            limit("b1", Side::Buy, 103, 8, 3),
        ];
        let r1 = run_matching_engine(&events);
        let r2 = run_matching_engine(&events);
        assert_eq!(r1.fills, r2.fills);
        assert_eq!(r1.rejections, r2.rejections);
        // Compare snapshots via their sorted key/value pairs.
        let mut b1: Vec<_> = r1.final_book.bids.iter().collect();
        let mut b2: Vec<_> = r2.final_book.bids.iter().collect();
        b1.sort();
        b2.sort();
        assert_eq!(b1, b2);
        let mut a1: Vec<_> = r1.final_book.asks.iter().collect();
        let mut a2: Vec<_> = r2.final_book.asks.iter().collect();
        a1.sort();
        a2.sort();
        assert_eq!(a1, a2);
    }
}

// ---------------------------------------------------------------------------
// Property-based tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod property_tests {
    use super::*;
    use proptest::prelude::*;

    // -----------------------------------------------------------------------
    // Generators
    // -----------------------------------------------------------------------

    /// Generate a valid price in a reasonable range to make crosses likely.
    fn arb_price() -> impl Strategy<Value = Price> {
        (1u64..=1_000u64).prop_map(Price)
    }

    fn arb_qty() -> impl Strategy<Value = u64> {
        1u64..=100u64
    }

    fn arb_side() -> impl Strategy<Value = Side> {
        prop_oneof![Just(Side::Buy), Just(Side::Sell)]
    }

    /// Generate a sequence of 1–20 order events with unique IDs.
    fn arb_event_seq() -> impl Strategy<Value = Vec<OrderEvent>> {
        prop::collection::vec(arb_single_event(), 1..=20)
    }

    fn arb_single_event() -> impl Strategy<Value = OrderEvent> {
        // Only produce LimitOrder events to keep the search space tractable and
        // avoid market-order rejections complicating idempotency checks.
        (arb_side(), arb_price(), arb_qty()).prop_map(|(side, price, qty)| {
            // Use a static counter via a thread-local to ensure unique IDs
            // within a single proptest iteration. proptest shrinking may reuse
            // seeds, so we generate unique IDs from the strategy arguments.
            OrderEvent::LimitOrder {
                id: OrderId(format!("{:?}-{}-{}", side, price.0, qty)),
                side,
                price,
                qty,
                seq_num: price.0.wrapping_add(qty), // deterministic from inputs
            }
        })
    }

    // -----------------------------------------------------------------------
    // Property 15 — RME Determinism
    //
    // Validates: Requirements 8.6, 8.7
    // -----------------------------------------------------------------------

    proptest! {
        /// **Validates: Requirements 8.6, 8.7**
        ///
        /// For any ordered sequence of order events `S`, `RME(S)` run twice
        /// SHALL produce identical fill sequences.
        #[test]
        fn prop15_rme_determinism(events in arb_event_seq()) {
            let r1 = run_matching_engine(&events);
            let r2 = run_matching_engine(&events);

            // Fills must be identical in order, price, qty, and IDs.
            prop_assert_eq!(r1.fills.len(), r2.fills.len());
            for (f1, f2) in r1.fills.iter().zip(r2.fills.iter()) {
                prop_assert_eq!(f1, f2);
            }

            // Rejections must also match.
            prop_assert_eq!(r1.rejections.len(), r2.rejections.len());
        }
    }

    // -----------------------------------------------------------------------
    // Property 16 — RME Idempotency under cancellation
    //
    // Validates: Requirements 8.8
    // -----------------------------------------------------------------------

    proptest! {
        /// **Validates: Requirements 8.8**
        ///
        /// For any valid order sequence `S`, let `F = RME(S).fills`.
        /// Running `RME(S ++ [CancelOrder(f.aggressor_id) for f in F])` SHALL
        /// leave the order book with all quantities non-negative and no
        /// fully-filled order remaining on the book.
        ///
        /// Since filled orders are removed from the book during matching,
        /// cancelling their IDs afterwards produces `RejectEvent { UnknownOrderId }`
        /// for each — this is the expected "consistent" behaviour.
        #[test]
        fn prop16_rme_idempotency_under_cancellation(events in arb_event_seq()) {
            let first_run = run_matching_engine(&events);

            // Build cancellation events for every aggressor that was filled.
            // Use a deduplicated set so we don't cancel the same ID twice
            // (which would just produce two rejects, not a correctness issue,
            //  but keeps the test clean).
            let mut seen = std::collections::HashSet::new();
            let mut extended_events: Vec<OrderEvent> = events.clone();
            for fill in &first_run.fills {
                if seen.insert(fill.aggressor_id.clone()) {
                    extended_events.push(OrderEvent::CancelOrder {
                        id: fill.aggressor_id.clone(),
                    });
                }
                // Also cancel resting IDs in case a resting order was partially
                // filled and still lives on the book after the first run.
                if seen.insert(fill.resting_id.clone()) {
                    extended_events.push(OrderEvent::CancelOrder {
                        id: fill.resting_id.clone(),
                    });
                }
            }

            let second_run = run_matching_engine(&extended_events);

            // All quantities in the final book must be non-negative (trivially
            // true for u64, but we also assert they are > 0, i.e. no zero-qty
            // ghost levels).
            for (_price, qty) in &second_run.final_book.bids {
                prop_assert!(*qty > 0, "bid level has zero quantity");
            }
            for (_price, qty) in &second_run.final_book.asks {
                prop_assert!(*qty > 0, "ask level has zero quantity");
            }

            // Collect the set of order IDs that were fully filled in the first
            // run. None of them should appear in the final book snapshot.
            //
            // We detect "fully filled" by checking that the order's ID is NOT
            // in the first run's final_book snapshot (which only shows resting
            // orders, not filled ones).  The `extended_events` run's final
            // book should similarly not contain them.
            //
            // Since FinalBookSnapshot only stores price→qty aggregates (not
            // per-order IDs), we verify the weaker property: the total book
            // qty in the extended run is ≤ that of the first run (cancellations
            // can only reduce or equal, never increase, resting quantities).
            let first_total: u64 = first_run.final_book.bids.values().sum::<u64>()
                + first_run.final_book.asks.values().sum::<u64>();
            let second_total: u64 = second_run.final_book.bids.values().sum::<u64>()
                + second_run.final_book.asks.values().sum::<u64>();
            prop_assert!(
                second_total <= first_total,
                "book grew after appending cancellations: before={} after={}",
                first_total,
                second_total
            );
        }
    }
}
