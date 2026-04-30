package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Harvey-AU/hover/internal/db"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	stripe "github.com/stripe/stripe-go/v82"
	stripesubscription "github.com/stripe/stripe-go/v82/subscription"
	"github.com/stripe/stripe-go/v82/webhook"
)

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
		log.Error().Msg("Stripe webhook secret not configured — rejecting event")
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}

	const maxBodyBytes = 65536
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to read Stripe webhook body")
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
		log.Warn().Err(err).Msg("Stripe webhook signature verification failed")
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	logger := log.With().Str("stripe_event_id", event.ID).Str("stripe_event_type", string(event.Type)).Logger()
	logger.Info().Msg("Received Stripe webhook event")

	var handlerErr error
	switch event.Type {
	case "checkout.session.completed":
		handlerErr = h.handleCheckoutSessionCompleted(r, event, logger)
	case "customer.subscription.updated":
		handlerErr = h.handleSubscriptionUpdated(r, event, logger)
	case "customer.subscription.deleted":
		handlerErr = h.handleSubscriptionDeleted(r, event, logger)
	case "invoice.payment_failed":
		h.handleInvoicePaymentFailed(event, logger)
	default:
		logger.Debug().Msg("Unhandled Stripe event type — ignoring")
	}

	if handlerErr != nil {
		logger.Error().Err(handlerErr).Msg("Stripe webhook handler reported transient failure — returning 5xx so Stripe retries")
		http.Error(w, "transient processing failure", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleCheckoutSessionCompleted(r *http.Request, event stripe.Event, logger zerolog.Logger) error {
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		logger.Error().Err(err).Msg("Failed to unmarshal checkout.session.completed")
		return nil
	}

	orgID := sess.ClientReferenceID
	if orgID == "" && sess.Customer != nil {
		id, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sess.Customer.ID)
		if err != nil {
			if errors.Is(err, db.ErrOrganisationNotFound) {
				logger.Warn().Str("customer_id", sess.Customer.ID).Msg("Unknown Stripe customer — ACKing event")
				return nil
			}
			logger.Error().Err(err).Str("customer_id", sess.Customer.ID).Msg("Cannot resolve organisation from Stripe customer")
			return fmt.Errorf("resolve organisation: %w", err)
		}
		orgID = id
	}
	if orgID == "" {
		logger.Error().Msg("checkout.session.completed: no organisation ID found — skipping")
		return nil
	}

	if sess.Customer != nil {
		if err := h.DB.SetStripeCustomerID(r.Context(), orgID, sess.Customer.ID); err != nil {
			logger.Error().Err(err).Str("org_id", orgID).Msg("Failed to store Stripe customer ID")
			return fmt.Errorf("set stripe customer id: %w", err)
		}
	}

	if sess.Subscription == nil {
		return nil
	}
	subID := sess.Subscription.ID
	if err := h.DB.SetStripeSubscriptionID(r.Context(), orgID, subID); err != nil {
		logger.Error().Err(err).Str("org_id", orgID).Msg("Failed to store Stripe subscription ID")
		return fmt.Errorf("set stripe subscription id: %w", err)
	}

	// The subscription in checkout.session.completed is not expanded —
	// fetch it directly to get the line items and price ID.
	sub, err := stripesubscription.Get(subID, nil)
	if err != nil {
		logger.Error().Err(err).Str("subscription_id", subID).Msg("Failed to fetch subscription from Stripe")
		return fmt.Errorf("fetch subscription: %w", err)
	}

	if len(sub.Items.Data) == 0 {
		logger.Error().Str("subscription_id", subID).Msg("Subscription has no line items — cannot activate plan")
		return nil
	}

	if sub.Items.Data[0].Price == nil {
		logger.Error().Str("subscription_id", subID).Msg("Subscription line item has no price — cannot activate plan")
		return nil
	}

	priceID := sub.Items.Data[0].Price.ID
	plan, err := h.DB.GetPlanByStripePriceID(r.Context(), priceID)
	if err != nil {
		if errors.Is(err, db.ErrPlanNotFound) {
			logger.Warn().Str("price_id", priceID).Msg("Stripe price has no matching local plan — ACKing event")
			return nil
		}
		logger.Error().Err(err).Str("price_id", priceID).Msg("Cannot resolve plan from Stripe price")
		return fmt.Errorf("resolve plan: %w", err)
	}
	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, plan.ID); err != nil {
		logger.Error().Err(err).Str("org_id", orgID).Str("plan_id", plan.ID).Msg("Failed to update organisation plan")
		return fmt.Errorf("set organisation plan: %w", err)
	}
	logger.Info().Str("org_id", orgID).Str("plan", plan.Name).Msg("Organisation plan activated via checkout")
	return nil
}

func (h *Handler) handleSubscriptionUpdated(r *http.Request, event stripe.Event, logger zerolog.Logger) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		logger.Error().Err(err).Msg("Failed to unmarshal customer.subscription.updated")
		return nil
	}

	if sub.Customer == nil {
		logger.Error().Msg("subscription.updated: missing customer — skipping")
		return nil
	}

	orgID, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sub.Customer.ID)
	if err != nil {
		if errors.Is(err, db.ErrOrganisationNotFound) {
			logger.Warn().Str("customer_id", sub.Customer.ID).Msg("Unknown Stripe customer — ACKing event")
			return nil
		}
		logger.Error().Err(err).Str("customer_id", sub.Customer.ID).Msg("Cannot resolve organisation")
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
		logger.Warn().Str("org_id", orgID).Str("event_subscription_id", sub.ID).Msg("Ignoring subscription.updated — no current subscription stored")
		return nil
	}
	if storedSubID != sub.ID {
		logger.Warn().Str("org_id", orgID).Str("event_subscription_id", sub.ID).Str("stored_subscription_id", storedSubID).Msg("Ignoring subscription.updated for non-current subscription")
		return nil
	}

	if len(sub.Items.Data) == 0 {
		logger.Warn().Str("org_id", orgID).Msg("subscription.updated: no line items — skipping plan update")
		return nil
	}

	if sub.Items.Data[0].Price == nil {
		logger.Warn().Str("org_id", orgID).Msg("subscription.updated: no price on line item — skipping plan update")
		return nil
	}

	priceID := sub.Items.Data[0].Price.ID
	plan, err := h.DB.GetPlanByStripePriceID(r.Context(), priceID)
	if err != nil {
		if errors.Is(err, db.ErrPlanNotFound) {
			logger.Warn().Str("price_id", priceID).Msg("Stripe price has no matching local plan — ACKing event")
			return nil
		}
		logger.Error().Err(err).Str("price_id", priceID).Msg("Cannot resolve plan from Stripe price")
		return fmt.Errorf("resolve plan: %w", err)
	}

	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, plan.ID); err != nil {
		logger.Error().Err(err).Str("org_id", orgID).Str("plan_id", plan.ID).Msg("Failed to update organisation plan")
		return fmt.Errorf("set organisation plan: %w", err)
	}
	logger.Info().Str("org_id", orgID).Str("plan", plan.Name).Msg("Organisation plan updated via subscription change")
	return nil
}

func (h *Handler) handleSubscriptionDeleted(r *http.Request, event stripe.Event, logger zerolog.Logger) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		logger.Error().Err(err).Msg("Failed to unmarshal customer.subscription.deleted")
		return nil
	}

	if sub.Customer == nil {
		logger.Error().Msg("subscription.deleted: missing customer — skipping")
		return nil
	}

	orgID, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sub.Customer.ID)
	if err != nil {
		if errors.Is(err, db.ErrOrganisationNotFound) {
			logger.Warn().Str("customer_id", sub.Customer.ID).Msg("Unknown Stripe customer — ACKing event")
			return nil
		}
		logger.Error().Err(err).Str("customer_id", sub.Customer.ID).Msg("Cannot resolve organisation")
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
		logger.Warn().Str("org_id", orgID).Str("event_subscription_id", sub.ID).Msg("Ignoring subscription.deleted — no current subscription stored")
		return nil
	}
	if storedSubID != sub.ID {
		logger.Warn().Str("org_id", orgID).Str("event_subscription_id", sub.ID).Str("stored_subscription_id", storedSubID).Msg("Ignoring subscription.deleted for non-current subscription")
		return nil
	}

	freePlanID, err := h.DB.GetFreePlanID(r.Context())
	if err != nil {
		logger.Error().Err(err).Msg("Failed to fetch free plan ID for subscription cancellation")
		return fmt.Errorf("fetch free plan: %w", err)
	}

	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, freePlanID); err != nil {
		logger.Error().Err(err).Str("org_id", orgID).Msg("Failed to revert organisation to free plan")
		return fmt.Errorf("revert to free plan: %w", err)
	}
	// Clear the stored subscription ID so a future Checkout creates a fresh
	// subscription rather than tripping the duplicate-subscription guard.
	if err := h.DB.SetStripeSubscriptionID(r.Context(), orgID, ""); err != nil {
		logger.Error().Err(err).Str("org_id", orgID).Msg("Failed to clear Stripe subscription ID after cancellation")
		return fmt.Errorf("clear stripe subscription id: %w", err)
	}
	logger.Info().Str("org_id", orgID).Msg("Organisation reverted to free plan — subscription cancelled")
	return nil
}

func (h *Handler) handleInvoicePaymentFailed(event stripe.Event, logger zerolog.Logger) {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		logger.Error().Err(err).Msg("Failed to unmarshal invoice.payment_failed")
		return
	}
	customerID := ""
	if inv.Customer != nil {
		customerID = inv.Customer.ID
	}
	logger.Warn().
		Str("invoice_id", inv.ID).
		Str("customer_id", customerID).
		Msg("Stripe invoice payment failed — Stripe will retry per dunning schedule")
}
