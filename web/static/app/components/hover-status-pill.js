/**
 * components/hover-status-pill.js — job and task status indicator
 *
 * Renders a small icon + label pair that reflects the current status of a
 * job or task. Used in job lists, job detail rows, and result cards across
 * the extension panel, dashboard, and any future surface.
 *
 * Usage (declarative):
 *   <hover-status-pill status="running"></hover-status-pill>
 *   <hover-status-pill status="completed" label="Done"></hover-status-pill>
 *   <hover-status-pill status="failed" variant="dot"></hover-status-pill>
 *
 * Attributes:
 *   status   — "pending" | "running" | "completed" | "failed" |
 *              "cancelled" | "skipped" | "queued" | "initializing" | "archived"
 *   label    — override the display label (optional; defaults to status map)
 *   variant  — "icon" (default, animated spinner/dot) | "dot" (plain dot only)
 *              | "label" (text only)
 *
 * Usage (imperative):
 *   import { createStatusPill } from "/app/components/hover-status-pill.js";
 *   const pill = createStatusPill("running");
 *   container.appendChild(pill);
 */

// ── Status maps ────────────────────────────────────────────────────────────────

/** @type {Record<string, string>} */
const STATUS_LABELS = {
  pending: "Starting up",
  queued: "Starting up",
  initializing: "Starting up",
  running: "In progress",
  in_progress: "In progress",
  processing: "In progress",
  completed: "Done",
  failed: "Error",
  error: "Error",
  cancelled: "Cancelled",
  cancelling: "Cancelling",
  skipped: "Skipped",
  archived: "Archived",
  blocked: "Blocked by site",
};

/** @type {Record<string, string>} Maps status → CSS modifier for the icon */
const STATUS_ICON_MOD = {
  pending: "pending",
  queued: "pending",
  initializing: "pending",
  running: "running",
  in_progress: "running",
  processing: "running",
  completed: "completed",
  failed: "error",
  error: "error",
  cancelled: "neutral",
  cancelling: "neutral",
  skipped: "neutral",
  archived: "neutral",
  blocked: "neutral",
};

/** @type {Record<string, string>} Maps status → CSS modifier for the colour */
const STATUS_COLOUR_MOD = {
  pending: "warning",
  queued: "warning",
  initializing: "warning",
  running: "warning",
  in_progress: "warning",
  processing: "warning",
  completed: "success",
  failed: "danger",
  error: "danger",
  cancelled: "neutral",
  cancelling: "neutral",
  skipped: "neutral",
  archived: "neutral",
  // "warning" amber rather than "danger": a WAF block is a site-side
  // decision, not a Hover failure — keeps the support framing right.
  blocked: "warning",
};

const VALID_VARIANTS = new Set(["icon", "dot", "label"]);

// ── Imperative helper ──────────────────────────────────────────────────────────

/**
 * Create a hover-status-pill element imperatively.
 * @param {string} status
 * @param {{ label?: string, variant?: "icon"|"dot"|"label" }} [options]
 * @returns {HoverStatusPill}
 */
export function createStatusPill(status, options = {}) {
  const el = /** @type {HoverStatusPill} */ (
    document.createElement("hover-status-pill")
  );
  el.setAttribute("status", status);
  if (options.label) el.setAttribute("label", options.label);
  if (options.variant) el.setAttribute("variant", options.variant);
  return el;
}

// ── Web Component ──────────────────────────────────────────────────────────────

class HoverStatusPill extends HTMLElement {
  static get observedAttributes() {
    return ["status", "label", "variant"];
  }

  connectedCallback() {
    this._render();
  }

  attributeChangedCallback() {
    if (this._rendered) this._render();
  }

  // ── Private ────────────────────────────────────────────────────────────────

  _render() {
    this._rendered = true;
    const status = (this.getAttribute("status") || "").toLowerCase();
    const label = this.getAttribute("label") || STATUS_LABELS[status] || status;
    const variant = VALID_VARIANTS.has(this.getAttribute("variant"))
      ? this.getAttribute("variant")
      : "icon";
    const colourMod = STATUS_COLOUR_MOD[status] || "neutral";
    const iconMod = STATUS_ICON_MOD[status] || "neutral";

    this.className = `hover-status-pill hover-status-pill--${colourMod}`;
    this.setAttribute("role", "status");
    this.setAttribute("aria-label", label);

    // Clear and rebuild
    this.innerHTML = "";

    if (variant === "icon" || variant === "dot") {
      const icon = document.createElement("span");
      icon.setAttribute("aria-hidden", "true");
      icon.className =
        variant === "dot"
          ? `hover-status-pill__dot hover-status-pill__dot--${colourMod}`
          : `hover-status-pill__icon hover-status-pill__icon--${iconMod}`;
      this.appendChild(icon);
    }

    if (variant !== "dot") {
      const text = document.createElement("span");
      text.className = "hover-status-pill__label";
      text.textContent = label;
      this.appendChild(text);
    }
  }
}

customElements.define("hover-status-pill", HoverStatusPill);

export default { createStatusPill };
