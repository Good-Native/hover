/**
 * lib/settings/organisations.js — org creation modal
 *
 * Handles the "Create Organisation" modal in settings.
 * Uses api-client for the POST request.
 */

import { createOrganisation } from "/app/lib/organisation-api.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

/**
 * Wire up the create-organisation modal within the settings page.
 * @param {object} [options]
 * @param {function} [options.onCreated] — called after successful creation
 */
export function initCreateOrgModal(options = {}) {
  const modal = document.getElementById("createOrgModal");
  const form = document.getElementById("createOrgForm");
  const nameInput = document.getElementById("newOrgName");
  const errorDiv = document.getElementById("createOrgError");
  const createBtn = document.getElementById("createOrgBtn");
  const closeBtn = document.getElementById("closeCreateOrgModal");
  const cancelBtn = document.getElementById("cancelCreateOrg");
  const submitBtn = document.getElementById("submitCreateOrg");

  if (!modal || !form) return;

  const openModal = () => {
    modal.classList.add("show");
    if (nameInput) nameInput.value = "";
    if (errorDiv) errorDiv.style.display = "none";
    nameInput?.focus();
  };

  const closeModal = () => {
    modal.classList.remove("show");
  };

  createBtn?.addEventListener("click", (e) => {
    e.stopPropagation();
    document.getElementById("orgSwitcher")?.classList.remove("open");
    openModal();
  });

  closeBtn?.addEventListener("click", closeModal);
  cancelBtn?.addEventListener("click", closeModal);
  modal?.addEventListener("click", (e) => {
    if (e.target === modal) closeModal();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && modal?.classList.contains("show")) {
      closeModal();
    }
  });

  form?.addEventListener("submit", async (e) => {
    e.preventDefault();

    const name = nameInput.value.trim();
    if (!name) {
      errorDiv.textContent = "Organisation name is required";
      errorDiv.style.display = "block";
      return;
    }

    submitBtn.disabled = true;
    submitBtn.textContent = "Creating...";
    errorDiv.style.display = "none";

    try {
      const newOrg = await createOrganisation(name);
      closeModal();

      // Update shared org data (bridge to legacy globals).
      window.GNH_ACTIVE_ORG = newOrg;
      if (Array.isArray(window.GNH_ORGANISATIONS)) {
        window.GNH_ORGANISATIONS.push(newOrg);
      } else {
        window.GNH_ORGANISATIONS = [newOrg];
      }

      document.dispatchEvent(
        new CustomEvent("gnh:org-switched", {
          detail: { organisation: newOrg },
        })
      );

      if (options.onCreated) await options.onCreated(newOrg);
      toast("success", `Organisation "${name}" created`);
    } catch (err) {
      console.error("Error creating organisation:", err);
      const message =
        err?.body?.message || "An error occurred. Please try again.";
      errorDiv.textContent = message;
      errorDiv.style.display = "block";
    } finally {
      submitBtn.disabled = false;
      submitBtn.textContent = "Create";
    }
  });
}
