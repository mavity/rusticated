use crate::boxed::Box;
use crate::future::Future;
use crate::pin::Pin;
use crate::task::{Context, Poll};

/// The output of [`select`]: whichever branch completed first.
pub enum Either<A, B> {
    /// The first (left) future completed first.
    Left(A),
    /// The second (right) future completed first.
    Right(B),
}

/// Drives two futures concurrently, resolving with whichever completes first.
///
/// Both sides are pinned on the heap. The losing future is dropped.
/// See [`select`] for construction.
pub struct Select<FA, FB> {
    a: Pin<Box<FA>>,
    b: Pin<Box<FB>>,
}

impl<FA: Future, FB: Future> Future for Select<FA, FB> {
    type Output = Either<FA::Output, FB::Output>;

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        // Poll left first; if it resolves, the right future is dropped.
        if let Poll::Ready(a) = self.a.as_mut().poll(cx) {
            return Poll::Ready(Either::Left(a));
        }
        // Poll right; if it resolves, the left future is dropped.
        if let Poll::Ready(b) = self.b.as_mut().poll(cx) {
            return Poll::Ready(Either::Right(b));
        }
        Poll::Pending
    }
}

/// Race two futures: resolve with whichever one completes first.
///
/// When both are immediately ready, the left wins (it is polled first).
/// The losing future is dropped when the [`Select`] future resolves.
///
/// # Example
///
/// ```rust,ignore
/// match std::rt::select(future_a, future_b).await {
///     std::rt::Either::Left(a)  => { /* a completed first */ }
///     std::rt::Either::Right(b) => { /* b completed first */ }
/// }
/// ```
pub fn select<FA: Future, FB: Future>(a: FA, b: FB) -> Select<FA, FB> {
    Select {
        a: Box::pin(a),
        b: Box::pin(b),
    }
}
