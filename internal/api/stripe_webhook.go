package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/logging"
	stripe "github.com/stripe/stripe-go/v82"
	stripesubscription "github.com/stripe/stripe-go/v82/subscription"
	"github.com/stripe/stripe-go/v82/webhook"
)

var webhookLog = logging.Component("stripe_webhook")

// StripeWebhook handles incoming Stripe webhook events.
// POST /v1/webhooks/stripe — no auth, signature verified internally.
//
// Handlers return a non-nil error only for transient failures (DB or Stripe API
// errors). Permanent failures — malformed payloads, missing required fields,
// unknown plan IDs — are logged and swallowed so Stripe stops retrying. We
// reply 5xx on transient errors so Stripe re-queues the event per its dunning
// schedule.
func (h *Handler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if h.StripeWebhookSecret == "" {
		webhookLog.ErrorContext(r.Context(), "Stripe webhook secret not configured — rejecting event")
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}

	const maxBodyBytes = 65536
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// Expected for oversized payloads from a misbehaving sender — don't
		// page Sentry; respond with a 400 so the sender can fix.
		webhookLog.WarnContext(logging.NoCapture(r.Context()), "Failed to read Stripe webhook body", "error", err)
		BadRequest(w, r, "Failed to read request body")
		return
	}

	// Tolerate API-version drift between the webhook destination's pinned
	// version and the stripe-go SDK's expected version. The destination's
	// version controls payload shape, not signing — and we deserialise
	// fields conservatively.
	event, err := webhook.ConstructEventWithOptions(
		body,
		r.Header.Get("Stripe-Signature"),
		h.StripeWebhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true},
	)
	if err != nil {
		// Signature failure is more interesting — keep Sentry capture so we
		// see if a misconfigured sender or a credential rotation breaks the
		// pipeline.
		webhookLog.WarnContext(r.Context(), "Stripe webhook signature verification failed", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	logger := webhookLog.With(
		"stripe_event_id", event.ID,
		"stripe_event_type", string(event.Type),
	)
	logger.InfoContext(r.Context(), "Received Stripe webhook event")

	var handlerErr error
	switch event.Type {
	case "checkout.session.completed":
		handlerErr = h.handleCheckoutSessionCompleted(r, event, logger)
	case "customer.subscription.updated":
		handlerErr = h.handleSubscriptionUpdated(r, event, logger)
	case "customer.subscription.deleted":
		handlerErr = h.handleSubscriptionDeleted(r, event, logger)
	case "invoice.payment_failed":
		h.handleInvoicePaymentFailed(r, event, logger)
	default:
		logger.DebugContext(r.Context(), "Unhandled Stripe event type — ignoring")
	}

	if handlerErr != nil {
		// Already logged at the failure site (with appropriate Sentry capture);
		// suppress capture here to avoid duplicating the issue.
		logger.ErrorContext(logging.NoCapture(r.Context()), "Stripe webhook handler reported transient failure — returning 5xx so Stripe retries", "error", handlerErr)
		http.Error(w, "transient processing failure", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleCheckoutSessionCompleted(r *http.Request, event stripe.Event, logger *logging.Logger) error {
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		logger.ErrorContext(r.Context(), "Failed to unmarshal checkout.session.completed", "error", err)
		return nil
	}

	orgID := sess.ClientReferenceID
	if orgID == "" && sess.Customer != nil {
		id, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sess.Customer.ID)
		if err != nil {
			if errors.Is(err, db.ErrOrganisationNotFound) {
				logger.WarnContext(logging.NoCapture(r.Context()), "Unknown Stripe customer — ACKing event", "customer_id", sess.Customer.ID)
				return nil
			}
			logger.ErrorContext(r.Context(), "Cannot resolve organisation from Stripe customer", "error", err, "customer_id", sess.Customer.ID)
			return fmt.Errorf("resolve organisation: %w", err)
		}
		orgID = id
	}
	if orgID == "" {
		logger.ErrorContext(r.Context(), "checkout.session.completed: no organisation ID found — skipping")
		return nil
	}

	if sess.Customer != nil {
		if err := h.DB.SetStripeCustomerID(r.Context(), orgID, sess.Customer.ID); err != nil {
			logger.ErrorContext(r.Context(), "Failed to store Stripe customer ID", "error", err, "org_id", orgID)
			return fmt.Errorf("set stripe customer id: %w", err)
		}
	}

	if sess.Subscription == nil {
		return nil
	}
	subID := sess.Subscription.ID
	if err := h.DB.SetStripeSubscriptionID(r.Context(), orgID, subID); err != nil {
		logger.ErrorContext(r.Context(), "Failed to store Stripe subscription ID", "error", err, "org_id", orgID)
		return fmt.Errorf("set stripe subscription id: %w", err)
	}

	// The subscription in checkout.session.completed is not expanded —
	// fetch it directly to get the line items and price ID.
	subFetchParams := &stripe.SubscriptionParams{}
	subFetchParams.Context = r.Context()
	sub, err := stripesubscription.Get(subID, subFetchParams)
	if err != nil {
		logger.ErrorContext(r.Context(), "Failed to fetch subscription from Stripe", "error", err, "subscription_id", subID)
		return fmt.Errorf("fetch subscription: %w", err)
	}

	if len(sub.Items.Data) == 0 {
		logger.ErrorContext(r.Context(), "Subscription has no line items — cannot activate plan", "subscription_id", subID)
		return nil
	}

	if sub.Items.Data[0].Price == nil {
		logger.ErrorContext(r.Context(), "Subscription line item has no price — cannot activate plan", "subscription_id", subID)
		return nil
	}

	priceID := sub.Items.Data[0].Price.ID
	plan, err := h.DB.GetPlanByStripePriceID(r.Context(), priceID)
	if err != nil {
		if errors.Is(err, db.ErrPlanNotFound) {
			logger.WarnContext(logging.NoCapture(r.Context()), "Stripe price has no matching local plan — ACKing event", "price_id", priceID)
			return nil
		}
		logger.ErrorContext(r.Context(), "Cannot resolve plan from Stripe price", "error", err, "price_id", priceID)
		return fmt.Errorf("resolve plan: %w", err)
	}
	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, plan.ID); err != nil {
		logger.ErrorContext(r.Context(), "Failed to update organisation plan", "error", err, "org_id", orgID, "plan_id", plan.ID)
		return fmt.Errorf("set organisation plan: %w", err)
	}
	logger.InfoContext(r.Context(), "Organisation plan activated via checkout", "org_id", orgID, "plan", plan.Name)
	return nil
}

func (h *Handler) handleSubscriptionUpdated(r *http.Request, event stripe.Event, logger *logging.Logger) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		logger.ErrorContext(r.Context(), "Failed to unmarshal customer.subscription.updated", "error", err)
		return nil
	}

	if sub.Customer == nil {
		logger.ErrorContext(r.Context(), "subscription.updated: missing customer — skipping")
		return nil
	}

	orgID, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sub.Customer.ID)
	if err != nil {
		if errors.Is(err, db.ErrOrganisationNotFound) {
			logger.WarnContext(logging.NoCapture(r.Context()), "Unknown Stripe customer — ACKing event", "customer_id", sub.Customer.ID)
			return nil
		}
		logger.ErrorContext(r.Context(), "Cannot resolve organisation", "error", err, "customer_id", sub.Customer.ID)
		return fmt.Errorf("resolve organisation: %w", err)
	}

	// Only act on events for the org's current subscription. Stripe events can
	// arrive late or out-of-order (e.g. an update for a long-canceled sub),
	// and adopting whichever event lands first as the source of truth would
	// reopen the very stale-event problem this guard exists to prevent.
	//
	// When no sub is stored, ignore the event entirely. Empty state means
	// either we never observed checkout.session.completed (which is the
	// authoritative seeder of stripe_subscription_id) or the user has already
	// cancelled. BillingCheckout's defensive orphan check (billing.go) heals
	// state on the next user action by listing Stripe subs and adopting the
	// active one — only then is the ID trustworthy.
	storedSubID, err := h.DB.GetStripeSubscriptionID(r.Context(), orgID)
	if err != nil {
		return fmt.Errorf("fetch stored subscription id: %w", err)
	}
	if storedSubID == "" {
		logger.WarnContext(logging.NoCapture(r.Context()), "Ignoring subscription.updated — no current subscription stored", "org_id", orgID, "event_subscription_id", sub.ID)
		return nil
	}
	if storedSubID != sub.ID {
		logger.WarnContext(logging.NoCapture(r.Context()), "Ignoring subscription.updated for non-current subscription", "org_id", orgID, "event_subscription_id", sub.ID, "stored_subscription_id", storedSubID)
		return nil
	}

	// Re-fetch the subscription from Stripe before reading the price. Stripe
	// doesn't guarantee webhook delivery order — a stale subscription.updated
	// can arrive after a newer one. Acting on the payload alone risks
	// overwriting the plan with an old price; the live Stripe object is the
	// only authoritative source.
	fetchParams := &stripe.SubscriptionParams{}
	fetchParams.Context = r.Context()
	freshSub, err := stripesubscription.Get(sub.ID, fetchParams)
	if err != nil {
		logger.ErrorContext(r.Context(), "Failed to refetch subscription from Stripe", "error", err, "subscription_id", sub.ID)
		return fmt.Errorf("refetch subscription: %w", err)
	}

	if freshSub.Items == nil || len(freshSub.Items.Data) == 0 {
		logger.WarnContext(r.Context(), "subscription.updated: no line items on refreshed subscription — skipping plan update", "org_id", orgID)
		return nil
	}

	if freshSub.Items.Data[0].Price == nil {
		logger.WarnContext(r.Context(), "subscription.updated: no price on refreshed line item — skipping plan update", "org_id", orgID)
		return nil
	}

	priceID := freshSub.Items.Data[0].Price.ID
	plan, err := h.DB.GetPlanByStripePriceID(r.Context(), priceID)
	if err != nil {
		if errors.Is(err, db.ErrPlanNotFound) {
			logger.WarnContext(logging.NoCapture(r.Context()), "Stripe price has no matching local plan — ACKing event", "price_id", priceID)
			return nil
		}
		logger.ErrorContext(r.Context(), "Cannot resolve plan from Stripe price", "error", err, "price_id", priceID)
		return fmt.Errorf("resolve plan: %w", err)
	}

	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, plan.ID); err != nil {
		logger.ErrorContext(r.Context(), "Failed to update organisation plan", "error", err, "org_id", orgID, "plan_id", plan.ID)
		return fmt.Errorf("set organisation plan: %w", err)
	}
	logger.InfoContext(r.Context(), "Organisation plan updated via subscription change", "org_id", orgID, "plan", plan.Name)
	return nil
}

func (h *Handler) handleSubscriptionDeleted(r *http.Request, event stripe.Event, logger *logging.Logger) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		logger.ErrorContext(r.Context(), "Failed to unmarshal customer.subscription.deleted", "error", err)
		return nil
	}

	if sub.Customer == nil {
		logger.ErrorContext(r.Context(), "subscription.deleted: missing customer — skipping")
		return nil
	}

	orgID, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sub.Customer.ID)
	if err != nil {
		if errors.Is(err, db.ErrOrganisationNotFound) {
			logger.WarnContext(logging.NoCapture(r.Context()), "Unknown Stripe customer — ACKing event", "customer_id", sub.Customer.ID)
			return nil
		}
		logger.ErrorContext(r.Context(), "Cannot resolve organisation", "error", err, "customer_id", sub.Customer.ID)
		return fmt.Errorf("resolve organisation: %w", err)
	}

	// Only act on events for the org's current subscription. Same rationale
	// as handleSubscriptionUpdated — a delete on a zombie sub mustn't
	// downgrade an org whose real paid sub is healthy.
	storedSubID, err := h.DB.GetStripeSubscriptionID(r.Context(), orgID)
	if err != nil {
		return fmt.Errorf("fetch stored subscription id: %w", err)
	}
	if storedSubID == "" {
		logger.WarnContext(logging.NoCapture(r.Context()), "Ignoring subscription.deleted — no current subscription stored", "org_id", orgID, "event_subscription_id", sub.ID)
		return nil
	}
	if storedSubID != sub.ID {
		logger.WarnContext(logging.NoCapture(r.Context()), "Ignoring subscription.deleted for non-current subscription", "org_id", orgID, "event_subscription_id", sub.ID, "stored_subscription_id", storedSubID)
		return nil
	}

	freePlanID, err := h.DB.GetFreePlanID(r.Context())
	if err != nil {
		logger.ErrorContext(r.Context(), "Failed to fetch free plan ID for subscription cancellation", "error", err)
		return fmt.Errorf("fetch free plan: %w", err)
	}

	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, freePlanID); err != nil {
		logger.ErrorContext(r.Context(), "Failed to revert organisation to free plan", "error", err, "org_id", orgID)
		return fmt.Errorf("revert to free plan: %w", err)
	}
	// Clear the stored subscription ID so a future Checkout creates a fresh
	// subscription rather than tripping the duplicate-subscription guard.
	if err := h.DB.SetStripeSubscriptionID(r.Context(), orgID, ""); err != nil {
		logger.ErrorContext(r.Context(), "Failed to clear Stripe subscription ID after cancellation", "error", err, "org_id", orgID)
		return fmt.Errorf("clear stripe subscription id: %w", err)
	}
	logger.InfoContext(r.Context(), "Organisation reverted to free plan — subscription cancelled", "org_id", orgID)
	return nil
}

func (h *Handler) handleInvoicePaymentFailed(r *http.Request, event stripe.Event, logger *logging.Logger) {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		logger.ErrorContext(r.Context(), "Failed to unmarshal invoice.payment_failed", "error", err)
		return
	}
	customerID := ""
	if inv.Customer != nil {
		customerID = inv.Customer.ID
	}
	// Customer-driven payment issue, not a system fault — keep out of Sentry.
	logger.WarnContext(logging.NoCapture(r.Context()), "Stripe invoice payment failed — Stripe will retry per dunning schedule", "invoice_id", inv.ID, "customer_id", customerID)
}
