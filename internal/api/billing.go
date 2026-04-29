package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"
	stripe "github.com/stripe/stripe-go/v82"
	portalsession "github.com/stripe/stripe-go/v82/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/customer"
)

// BillingCheckout creates a Stripe Checkout Session for a plan upgrade.
// POST /v1/billing/checkout
// Body: {"plan_id": "<uuid>"}
// Returns: {"url": "https://checkout.stripe.com/..."}
func (h *Handler) BillingCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if h.StripeSecretKey == "" {
		log.Error().Msg("Stripe secret key not configured")
		InternalError(w, r, fmt.Errorf("billing not configured"))
		return
	}

	user, orgID, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return
	}

	role, err := h.DB.GetOrganisationMemberRole(r.Context(), user.ID, orgID)
	if err != nil {
		log.Error().Err(err).Str("org_id", orgID).Str("user_id", user.ID).Msg("Failed to look up organisation member role")
		InternalError(w, r, fmt.Errorf("failed to verify membership"))
		return
	}
	if role != "admin" {
		Forbidden(w, r, "Only organisation admins can manage billing")
		return
	}

	// Stripe Checkout in subscription mode always creates a new subscription.
	// Existing subscribers must use the BillingPortal to change plans, otherwise
	// we end up with duplicate live subscriptions on the customer.
	existingSubID, err := h.DB.GetStripeSubscriptionID(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to check existing subscription: %w", err))
		return
	}
	if existingSubID != "" {
		Conflict(w, r, "Organisation already has an active subscription — use the billing portal to change plans")
		return
	}

	var body struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PlanID == "" {
		BadRequest(w, r, "plan_id is required")
		return
	}

	// Fetch all plans to find the requested one with its Stripe Price ID.
	plans, err := h.DB.GetActivePlans(r.Context())
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to fetch plans: %w", err))
		return
	}
	var stripePriceID string
	for _, p := range plans {
		if p.ID == body.PlanID {
			stripePriceID = p.StripePriceID
			break
		}
	}
	if stripePriceID == "" {
		BadRequest(w, r, "Plan not found or not available for purchase")
		return
	}

	// Get or create Stripe Customer for this organisation.
	customerID, err := h.DB.GetStripeCustomerID(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to fetch stripe customer: %w", err))
		return
	}
	if customerID == "" {
		org, err := h.DB.GetOrganisation(orgID)
		if err != nil {
			InternalError(w, r, fmt.Errorf("failed to fetch organisation: %w", err))
			return
		}
		cust, err := customer.New(&stripe.CustomerParams{
			Email: stripe.String(user.Email),
			Name:  stripe.String(org.Name),
			Metadata: map[string]string{
				"organisation_id": orgID,
			},
		})
		if err != nil {
			log.Error().Err(err).Str("org_id", orgID).Msg("Failed to create Stripe customer")
			InternalError(w, r, fmt.Errorf("failed to create billing customer"))
			return
		}
		customerID = cust.ID
		log.Info().Str("customer_id", customerID).Str("org_id", orgID).Msg("Created Stripe customer")
		if err := h.DB.SetStripeCustomerID(r.Context(), orgID, customerID); err != nil {
			log.Error().Err(err).Str("org_id", orgID).Msg("Failed to store Stripe customer ID")
			InternalError(w, r, fmt.Errorf("failed to store billing customer"))
			return
		}
	}

	// Land back on the plans tab so the success toast (settings.js
	// handleBillingRedirect) renders next to the new plan card.
	baseURL := h.absoluteBaseURL(r)
	successURL := baseURL + "/settings/plans"
	cancelURL := baseURL + "/settings/plans"

	sess, err := checkoutsession.New(&stripe.CheckoutSessionParams{
		Customer: stripe.String(customerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(stripePriceID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL:        stripe.String(successURL + "?billing=success"),
		CancelURL:         stripe.String(cancelURL + "?billing=cancelled"),
		ClientReferenceID: stripe.String(orgID),
	})
	if err != nil {
		log.Error().Err(err).Str("org_id", orgID).Msg("Failed to create Stripe Checkout Session")
		InternalError(w, r, fmt.Errorf("failed to create checkout session"))
		return
	}

	WriteSuccess(w, r, map[string]string{"url": sess.URL}, "Checkout session created")
}

// BillingPortal creates a Stripe Billing Portal session for managing subscriptions.
// POST /v1/billing/portal
// Returns: {"url": "https://billing.stripe.com/..."}
func (h *Handler) BillingPortal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if h.StripeSecretKey == "" {
		log.Error().Msg("Stripe secret key not configured")
		InternalError(w, r, fmt.Errorf("billing not configured"))
		return
	}

	user, orgID, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return
	}

	role, err := h.DB.GetOrganisationMemberRole(r.Context(), user.ID, orgID)
	if err != nil {
		log.Error().Err(err).Str("org_id", orgID).Str("user_id", user.ID).Msg("Failed to look up organisation member role")
		InternalError(w, r, fmt.Errorf("failed to verify membership"))
		return
	}
	if role != "admin" {
		Forbidden(w, r, "Only organisation admins can manage billing")
		return
	}

	customerID, err := h.DB.GetStripeCustomerID(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to fetch billing info: %w", err))
		return
	}
	if customerID == "" {
		BadRequest(w, r, "No billing account found — upgrade to a paid plan first")
		return
	}

	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(h.absoluteBaseURL(r) + "/settings/plans"),
	}
	// Optional override — when unset, Stripe falls back to the account's
	// default Customer Portal configuration.
	if h.StripePortalConfigID != "" {
		params.Configuration = stripe.String(h.StripePortalConfigID)
	}
	sess, err := portalsession.New(params)
	if err != nil {
		log.Error().Err(err).Str("org_id", orgID).Msg("Failed to create Stripe Portal Session")
		InternalError(w, r, fmt.Errorf("failed to create portal session"))
		return
	}

	WriteSuccess(w, r, map[string]string{"url": sess.URL}, "Portal session created")
}

// absoluteBaseURL returns the scheme+host base URL for this request.
// Uses X-Forwarded-Proto when behind a proxy, falls back to https.
func (h *Handler) absoluteBaseURL(r *http.Request) string {
	if h.SettingsURL != "" {
		// Strip trailing /settings if the caller stored the full page URL.
		base := h.SettingsURL
		if len(base) > 9 && base[len(base)-9:] == "/settings" {
			base = base[:len(base)-9]
		}
		return base
	}
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}
