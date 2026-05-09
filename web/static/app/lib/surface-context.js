/**
 * lib/surface-context.js — shared surface-mode helpers
 *
 * Supports app pages opened from the Webflow extension panel by preserving
 * context in query params and exposing a small in-page back control.
 */

const SEARCH_PARAMS = new URLSearchParams(window.location.search);
const SURFACE = SEARCH_PARAMS.get("surface") || "";
const RETURN_TO_PARAM = SEARCH_PARAMS.get("return_to") || "";

function isAllowedProtocol(url) {
  return url.protocol === "http:" || url.protocol === "https:";
}

function isAllowedReturnHost(url) {
  return (
    url.hostname === "localhost" ||
    url.hostname === "127.0.0.1" ||
    url.hostname === "hover.app.goodnative.co" ||
    /^hover-pr-\d+\.fly\.dev$/i.test(url.hostname)
  );
}

export function isWebflowExtensionSurface() {
  return SURFACE === "webflow-extension";
}

export function getSafeReturnToUrl() {
  if (!RETURN_TO_PARAM) {
    return "";
  }

  try {
    const parsed = new URL(RETURN_TO_PARAM, window.location.origin);
    if (!isAllowedProtocol(parsed) || !isAllowedReturnHost(parsed)) {
      return "";
    }
    return parsed.toString();
  } catch {
    return "";
  }
}

export function buildSurfaceAwareUrl(pathOrUrl) {
  const target = new URL(pathOrUrl, window.location.origin);
  if (!isWebflowExtensionSurface()) {
    return target.toString();
  }

  target.searchParams.set("surface", SURFACE);
  const returnTo = getSafeReturnToUrl();
  if (returnTo) {
    target.searchParams.set("return_to", returnTo);
  }
  return target.toString();
}

export function rewriteSurfaceLinks(elements) {
  if (!isWebflowExtensionSurface()) {
    return;
  }

  elements.forEach((element) => {
    if (!(element instanceof HTMLAnchorElement)) {
      return;
    }

    const href = element.getAttribute("href") || "";
    if (!href || href.startsWith("#") || href.startsWith("mailto:")) {
      return;
    }

    try {
      const next = new URL(href, window.location.origin);
      if (next.origin !== window.location.origin) {
        return;
      }
      element.href = buildSurfaceAwareUrl(next.toString());
    } catch {
      // ignore malformed links
    }
  });
}

export function initSurfacePage(options = {}) {
  if (!isWebflowExtensionSurface()) {
    return null;
  }

  const title = options.title || "Hover";
  const defaultReturnPath = options.defaultReturnPath || "/dashboard";
  const safeReturnTo = getSafeReturnToUrl() || defaultReturnPath;

  document.documentElement.setAttribute("data-surface-context", SURFACE);

  const bar = document.getElementById("surfaceContextBar");
  const titleNode = document.getElementById("surfaceContextTitle");
  const subtitleNode = document.getElementById("surfaceContextSubtitle");
  const backButton = document.getElementById("surfaceContextBack");

  if (bar) {
    bar.hidden = false;
  }
  if (titleNode) {
    titleNode.textContent = title;
  }
  if (subtitleNode) {
    subtitleNode.textContent = "Opened from the Webflow extension";
  }
  if (backButton instanceof HTMLAnchorElement) {
    backButton.href = safeReturnTo;
  } else if (backButton instanceof HTMLButtonElement) {
    backButton.addEventListener("click", () => {
      window.location.assign(safeReturnTo);
    });
  }

  return {
    surface: SURFACE,
    returnTo: safeReturnTo,
  };
}

export default {
  buildSurfaceAwareUrl,
  getSafeReturnToUrl,
  initSurfacePage,
  isWebflowExtensionSurface,
  rewriteSurfaceLinks,
};
