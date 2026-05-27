/// OrderBook implements a price-time priority order book using sorted
/// price levels (BTreeMap) and FIFO queues (VecDeque) within each level.
///
/// Bids are stored in descending price order (highest bid first).
/// Asks are stored in ascending price order (lowest ask first).
/// A cancel index (`HashMap<OrderId, (Side, Price)>`) enables O(1) lookup
/// for cancel and replace operations.
///
/// Full matching logic is implemented in Task 10.
use std::collections::{BTreeMap, HashMap, VecDeque};

use crate::{Order, OrderId, Price, Side};

/// A single side of the order book: sorted price levels → FIFO order queue.
#[derive(Debug, Default, Clone)]
pub struct BookSide {
    /// Price level → FIFO queue of resting orders at that price.
    /// For bids: keys are wrapped in `Reverse` by the caller to achieve
    /// descending order. For asks: keys are stored directly (ascending).
    pub levels: BTreeMap<u64, VecDeque<Order>>,
}

impl BookSide {
    pub fn new() -> Self {
        Self {
            levels: BTreeMap::new(),
        }
    }

    /// Insert an order at its price level (appended to the back of the queue
    /// to maintain time priority within a price level).
    pub fn insert(&mut self, price: Price, order: Order) {
        self.levels.entry(price.0).or_default().push_back(order);
    }

    /// Remove a specific order by ID from its price level.
    /// Returns `true` if the order was found and removed.
    pub fn remove(&mut self, price: Price, order_id: &OrderId) -> bool {
        if let Some(queue) = self.levels.get_mut(&price.0) {
            if let Some(pos) = queue.iter().position(|o| &o.id == order_id) {
                queue.remove(pos);
                if queue.is_empty() {
                    self.levels.remove(&price.0);
                }
                return true;
            }
        }
        false
    }

    /// Peek at the best (front) order at the best price level.
    pub fn best(&self) -> Option<(&u64, &Order)> {
        self.levels
            .iter()
            .next()
            .and_then(|(price, queue)| queue.front().map(|o| (price, o)))
    }

    /// Mutably peek at the best order at the best price level.
    pub fn best_mut(&mut self) -> Option<&mut Order> {
        self.levels
            .iter_mut()
            .next()
            .and_then(|(_, queue)| queue.front_mut())
    }

    /// Pop (remove and return) the best order.
    pub fn pop_best(&mut self) -> Option<Order> {
        let best_price = *self.levels.keys().next()?;
        let queue = self.levels.get_mut(&best_price)?;
        let order = queue.pop_front();
        if queue.is_empty() {
            self.levels.remove(&best_price);
        }
        order
    }
}

/// Full two-sided order book with a cancel index.
#[derive(Debug, Default, Clone)]
pub struct OrderBook {
    /// Bids: stored with price key = `u64::MAX - raw_price` to achieve
    /// descending order via BTreeMap's natural ascending iteration.
    pub bids: BookSide,
    /// Asks: stored with price key = `raw_price` for ascending order.
    pub asks: BookSide,
    /// Cancel index: order_id → (Side, raw_price) for O(1) removal.
    pub cancel_index: HashMap<OrderId, (Side, Price)>,
}

impl OrderBook {
    pub fn new() -> Self {
        Self::default()
    }

    /// Add a limit order to the resting book (does not attempt matching).
    pub fn add_limit(&mut self, order: Order) {
        let price = order.price;
        let side = order.side;
        let id = order.id.clone();
        match side {
            Side::Buy => {
                // Invert price so that BTreeMap iteration gives descending order.
                let inv = u64::MAX - price.0;
                self.bids.levels.entry(inv).or_default().push_back(order);
                self.cancel_index.insert(id, (Side::Buy, price));
            }
            Side::Sell => {
                self.asks.insert(price, order);
                self.cancel_index.insert(id, (Side::Sell, price));
            }
        }
    }

    /// Cancel a resting order by ID. Returns `true` if found.
    pub fn cancel(&mut self, order_id: &OrderId) -> bool {
        if let Some((side, price)) = self.cancel_index.remove(order_id) {
            match side {
                Side::Buy => {
                    let inv = u64::MAX - price.0;
                    let inv_price = Price(inv);
                    self.bids.remove(inv_price, order_id)
                }
                Side::Sell => self.asks.remove(price, order_id),
            }
        } else {
            false
        }
    }

    /// Return the highest bid price (raw). The bid side stores `u64::MAX - raw_price`
    /// as the BTreeMap key to achieve descending iteration; un-invert here.
    pub fn best_bid(&self) -> Option<Price> {
        self.bids
            .levels
            .keys()
            .next()
            .map(|inv_key| Price(u64::MAX - inv_key))
    }

    /// Return the lowest ask price (raw).
    pub fn best_ask(&self) -> Option<Price> {
        self.asks
            .levels
            .keys()
            .next()
            .copied()
            .map(Price)
    }

    /// Total resting quantity at a bid price level.
    pub fn bid_qty_at(&self, price: Price) -> u64 {
        let inv = u64::MAX - price.0;
        self.bids
            .levels
            .get(&inv)
            .map(|q| q.iter().map(|o| o.qty).sum())
            .unwrap_or(0)
    }

    /// Total resting quantity at an ask price level.
    pub fn ask_qty_at(&self, price: Price) -> u64 {
        self.asks
            .levels
            .get(&price.0)
            .map(|q| q.iter().map(|o| o.qty).sum())
            .unwrap_or(0)
    }

    /// True if there are no resting orders on either side.
    pub fn is_empty(&self) -> bool {
        self.bids.levels.is_empty() && self.asks.levels.is_empty()
    }
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{Order, OrderId, Price, Side};

    fn make_order(id: &str, side: Side, price: u64, qty: u64, seq: u64) -> Order {
        Order {
            id: OrderId(id.to_string()),
            side,
            price: Price(price),
            qty,
            seq_num: seq,
        }
    }

    #[test]
    fn test_bid_insertion_and_best_bid() {
        let mut book = OrderBook::new();
        book.add_limit(make_order("b1", Side::Buy, 100, 10, 1));
        assert_eq!(book.best_bid(), Some(Price(100)));
    }

    #[test]
    fn test_ask_insertion_and_best_ask() {
        let mut book = OrderBook::new();
        book.add_limit(make_order("a1", Side::Sell, 101, 5, 1));
        assert_eq!(book.best_ask(), Some(Price(101)));
    }

    #[test]
    fn test_cancel_removes_from_book_and_index() {
        let mut book = OrderBook::new();
        let id = OrderId("b1".to_string());
        book.add_limit(make_order("b1", Side::Buy, 100, 10, 1));
        assert!(book.cancel_index.contains_key(&id));
        let removed = book.cancel(&id);
        assert!(removed);
        assert!(!book.cancel_index.contains_key(&id));
        assert_eq!(book.best_bid(), None);
    }

    #[test]
    fn test_cancel_unknown_order_returns_false() {
        let mut book = OrderBook::new();
        let id = OrderId("ghost".to_string());
        assert!(!book.cancel(&id));
    }

    #[test]
    fn test_bid_price_descending_order() {
        let mut book = OrderBook::new();
        // Insert lower price first to confirm descending iteration.
        book.add_limit(make_order("b1", Side::Buy, 99, 10, 1));
        book.add_limit(make_order("b2", Side::Buy, 101, 10, 2));
        book.add_limit(make_order("b3", Side::Buy, 100, 10, 3));

        // Collect stored keys (inverted); un-invert and check descending.
        let raw_prices: Vec<u64> = book
            .bids
            .levels
            .keys()
            .map(|k| u64::MAX - k)
            .collect();
        assert_eq!(raw_prices, vec![101, 100, 99]);
        assert_eq!(book.best_bid(), Some(Price(101)));
    }

    #[test]
    fn test_ask_price_ascending_order() {
        let mut book = OrderBook::new();
        book.add_limit(make_order("a1", Side::Sell, 103, 5, 1));
        book.add_limit(make_order("a2", Side::Sell, 101, 5, 2));
        book.add_limit(make_order("a3", Side::Sell, 102, 5, 3));

        let raw_prices: Vec<u64> = book.asks.levels.keys().copied().collect();
        assert_eq!(raw_prices, vec![101, 102, 103]);
        assert_eq!(book.best_ask(), Some(Price(101)));
    }

    #[test]
    fn test_bid_qty_at() {
        let mut book = OrderBook::new();
        book.add_limit(make_order("b1", Side::Buy, 100, 7, 1));
        book.add_limit(make_order("b2", Side::Buy, 100, 3, 2));
        assert_eq!(book.bid_qty_at(Price(100)), 10);
        assert_eq!(book.bid_qty_at(Price(99)), 0);
    }

    #[test]
    fn test_ask_qty_at() {
        let mut book = OrderBook::new();
        book.add_limit(make_order("a1", Side::Sell, 101, 8, 1));
        book.add_limit(make_order("a2", Side::Sell, 101, 2, 2));
        assert_eq!(book.ask_qty_at(Price(101)), 10);
        assert_eq!(book.ask_qty_at(Price(102)), 0);
    }

    #[test]
    fn test_is_empty_initially_and_after_add() {
        let mut book = OrderBook::new();
        assert!(book.is_empty());
        book.add_limit(make_order("b1", Side::Buy, 100, 10, 1));
        assert!(!book.is_empty());
    }
}
