package metis

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// DeliveryStore remembers which deliveries a receiver has handled, so one
// that arrives again is answered without being handled twice.
//
// Metis delivers at least once: a delivery whose 2xx was lost on the way back
// is sent again with the same X-Delivery-Id. [NewMemoryDeliveryStore] keeps
// the IDs in the process, which is enough for a single receiver. Behind a
// load balancer the same delivery can reach two receivers, so back the store
// with something they share, a database table keyed by the ID, say.
type DeliveryStore interface {
	// Handled reports whether the delivery was handled already.
	Handled(ctx context.Context, deliveryID string) (bool, error)
	// MarkHandled records that the delivery was handled.
	MarkHandled(ctx context.Context, deliveryID string) error
}

// MemoryDeliveryStore is a [DeliveryStore] that keeps the most recent IDs in
// memory, forgetting the oldest beyond its capacity. It is safe for
// concurrent use.
type MemoryDeliveryStore struct {
	mu       sync.Mutex
	capacity int
	order    *list.List
	ids      map[string]*list.Element
}

// NewMemoryDeliveryStore keeps up to capacity delivery IDs; zero or less
// means 10,000. Metis's own retries of a delivery end within a day, so a
// capacity that covers a day of deliveries catches them. A delivery sent
// again by hand with [Client.RetryOutgoingWebhookDelivery], up to 30 days
// later, is only caught by a store that keeps it that long.
func NewMemoryDeliveryStore(capacity int) *MemoryDeliveryStore {
	if capacity <= 0 {
		capacity = 10_000
	}
	return &MemoryDeliveryStore{capacity: capacity, order: list.New(), ids: make(map[string]*list.Element)}
}

// Handled reports whether id is remembered.
func (s *MemoryDeliveryStore) Handled(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	return ok, nil
}

// MarkHandled remembers id, forgetting the oldest one when full.
func (s *MemoryDeliveryStore) MarkHandled(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if element, ok := s.ids[id]; ok {
		s.order.MoveToFront(element)
		return nil
	}
	s.ids[id] = s.order.PushFront(id)
	if s.order.Len() > s.capacity {
		oldest := s.order.Back()
		s.order.Remove(oldest)
		delete(s.ids, oldest.Value.(string))
	}
	return nil
}

// WebhookHandlerOptions configures [WebhookHandler].
type WebhookHandlerOptions struct {
	// Store remembers handled deliveries. Nil means a
	// [NewMemoryDeliveryStore] of the default size.
	Store DeliveryStore
	// OnError, when set, is told every delivery that was refused or whose
	// handling failed, with why — for a log line. The answer to Metis does
	// not carry the reason.
	OnError func(r *http.Request, err error)
}

// WebhookHandler is an http.Handler for an outgoing webhook's address. It
// verifies each delivery with the webhook's secret, answers a repeat of one
// already handled without handling it again, and calls handle with the rest:
//
//	http.Handle("/hooks/metis", metis.WebhookHandler(secret,
//		metis.WebhookHandlers{
//			metis.EventTaskCreated:    notifyAssignee,
//			metis.EventIncidentRaised: pageOnCall,
//		}.Handle,
//		metis.WebhookHandlerOptions{}))
//
// It answers:
//   - 204 when handle returned nil, or the delivery was handled before;
//   - 401 for a delivery not signed with secret, or signed too long ago;
//   - 400 for a body that is not a delivery, 405 for anything but a POST;
//   - 409 while the same delivery is being handled by another request here,
//     and 500 when handle or the store failed, so that Metis sends it again
//     later.
//
// handle runs within the request, and Metis waits ten seconds for the answer
// before it counts the attempt as failed and tries again. Hand longer work to
// a queue and return. A delivery is marked handled only after handle returns
// nil, so make handle safe to run twice for one delivery: the store is
// checked first, but two receivers sharing no store, or a crash between
// handling and marking, can still run it twice.
func WebhookHandler(secret string, handle func(context.Context, *Delivery) error, opts WebhookHandlerOptions) http.Handler {
	store := opts.Store
	if store == nil {
		store = NewMemoryDeliveryStore(0)
	}
	return &webhookHandler{secret: secret, handle: handle, store: store, onError: opts.OnError, busy: make(map[string]struct{})}
}

type webhookHandler struct {
	secret  string
	handle  func(context.Context, *Delivery) error
	store   DeliveryStore
	onError func(*http.Request, error)

	mu   sync.Mutex
	busy map[string]struct{}
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.fail(w, r, http.StatusMethodNotAllowed, fmt.Errorf("metis: a webhook delivery is a POST, not a %s", r.Method))
		return
	}
	delivery, err := ReadWebhook(r, h.secret)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrBadSignature) || errors.Is(err, ErrStaleSignature) {
			status = http.StatusUnauthorized
		}
		h.fail(w, r, status, err)
		return
	}

	if !h.begin(delivery.DeliveryID) {
		h.fail(w, r, http.StatusConflict, fmt.Errorf("metis: delivery %s is already being handled", delivery.DeliveryID))
		return
	}
	defer h.end(delivery.DeliveryID)

	ctx := r.Context()
	handled, err := h.store.Handled(ctx, delivery.DeliveryID)
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, fmt.Errorf("metis: look up delivery %s: %w", delivery.DeliveryID, err))
		return
	}
	if !handled {
		if err := h.handle(ctx, delivery); err != nil {
			h.fail(w, r, http.StatusInternalServerError, fmt.Errorf("metis: handle delivery %s: %w", delivery.DeliveryID, err))
			return
		}
		if err := h.store.MarkHandled(ctx, delivery.DeliveryID); err != nil {
			// Handled, and not remembered: a repeat will be handled again,
			// which handle is required to bear. Not reported as a failure,
			// which would make Metis send it again for certain.
			h.report(r, fmt.Errorf("metis: remember delivery %s: %w", delivery.DeliveryID, err))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// begin marks id as in hand here, false when it already was.
func (h *webhookHandler) begin(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, taken := h.busy[id]; taken {
		return false
	}
	h.busy[id] = struct{}{}
	return true
}

func (h *webhookHandler) end(id string) {
	h.mu.Lock()
	delete(h.busy, id)
	h.mu.Unlock()
}

func (h *webhookHandler) fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	h.report(r, err)
	http.Error(w, http.StatusText(status), status)
}

func (h *webhookHandler) report(r *http.Request, err error) {
	if h.onError != nil {
		h.onError(r, err)
	}
}

// WebhookHandlers routes a delivery to the function for its event. A
// delivery of an event with no function is accepted and left alone, so a
// webhook that gains an event does not fail every delivery of it.
type WebhookHandlers map[WebhookEvent]func(context.Context, *Delivery) error

// Handle calls the function for the delivery's event, if there is one. Pass
// it to [WebhookHandler].
func (hs WebhookHandlers) Handle(ctx context.Context, d *Delivery) error {
	if handle := hs[d.Event]; handle != nil {
		return handle(ctx, d)
	}
	return nil
}
