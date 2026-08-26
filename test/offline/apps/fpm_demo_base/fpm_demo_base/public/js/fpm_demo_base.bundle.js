// Entry point built by esbuild into public/dist/js/fpm_demo_base.bundle.<HASH>.js.
// Besides the marker, it renders a banner on every page so a browser shows
// visibly that this app's bundle was loaded through sites/assets/assets.json.
import "./fpm_demo_base_lib.js";
window.fpm_demo_base_loaded = "FPM_DEMO_BASE_MARKER";
const fpm_demo_base_src = (document.currentScript && document.currentScript.src) || "";
function fpm_demo_base_banner() {
	if (document.getElementById("fpm_demo_base-banner")) return;
	const el = document.createElement("div");
	el.id = "fpm_demo_base-banner";
	el.className = "fpm_demo_base-banner";
	el.textContent = "✅ FPM Demo Base v1.0.0 — JS bundle loaded from " + fpm_demo_base_src.replace(/^https?:\/\/[^/]+/, "") + " (installed offline by fpm)";
	document.body.prepend(el);
}
if (document.readyState === "loading") {
	document.addEventListener("DOMContentLoaded", fpm_demo_base_banner);
} else {
	fpm_demo_base_banner();
}
console.log("FPM_DEMO_BASE_MARKER loaded");
