// Browser-level proof for the offline install: drives a headless Chromium that shares
// the isolated bench container's network namespace (so it can reach 127.0.0.1:8000 but,
// like the bench, has no outbound network), and records what a user would see.
//
// Writes to /out:
//   ui-login.png            the website /login page
//   ui-desk.png             the desk (/app/home) after logging in as Administrator
//   ui-installed-apps.png   the desk's Installed Applications list
//   ui-report.json          markers set by the apps' JS bundles, computed CSS from their
//                           stylesheets, every /assets/fpm_demo_* response and its status,
//                           and frappe.boot.assets_json entries for the apps
const puppeteer = require("puppeteer");
const fs = require("fs");

const BASE = process.env.SITE_URL || "http://127.0.0.1:8000";
const APPS = ["fpm_demo_base", "fpm_demo_child"];
const OUT = "/out";

(async () => {
	const browser = await puppeteer.launch({
		executablePath: process.env.CHROME_BIN || "/usr/bin/chromium-browser",
		args: ["--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--window-size=1280,900"],
	});
	const page = await browser.newPage();
	await page.setViewport({ width: 1280, height: 900 });

	const report = { base: BASE, assetResponses: [], console: [], pages: {} };
	page.on("response", (r) => {
		const u = r.url();
		if (u.includes("/assets/fpm_demo_")) report.assetResponses.push({ status: r.status(), url: u.replace(BASE, "") });
	});
	page.on("console", (m) => report.console.push(m.text()));

	const probe = async () =>
		page.evaluate((apps) => {
			const out = { markers: {}, banners: {}, css: {} };
			for (const app of apps) {
				out.markers[app] = window[app + "_loaded"] || null;
				const banner = document.getElementById(app + "-banner");
				out.banners[app] = banner ? banner.textContent : null;
				const probeEl = document.createElement("div");
				probeEl.className = app + "-banner";
				document.body.appendChild(probeEl);
				const cs = getComputedStyle(probeEl);
				out.css[app] = { background: cs.backgroundColor, color: cs.color, position: cs.position };
				probeEl.remove();
			}
			return out;
		}, APPS);

	// 1. Website page: web_include_js / web_include_css.
	await page.goto(BASE + "/login", { waitUntil: "networkidle0", timeout: 180000 });
	report.pages.login = await probe();
	await page.screenshot({ path: OUT + "/ui-login.png" });

	// 2. Log in. The form's markup varies between Frappe versions, so authenticate the
	// way the form itself does — a POST to /api/method/login from the page, which sets
	// the session cookie in this browser context.
	const login = await page.evaluate(async (pwd) => {
		const r = await fetch("/api/method/login", {
			method: "POST",
			headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
			body: "usr=Administrator&pwd=" + encodeURIComponent(pwd),
		});
		return { status: r.status, body: await r.text() };
	}, process.env.ADMIN_PASSWORD || "admin");
	report.login = login;
	if (login.status !== 200) throw new Error("login failed: " + login.status + " " + login.body);

	// 3. Desk: app_include_js / app_include_css, resolved through frappe.boot.assets_json.
	page.on("pageerror", (e) => report.console.push("PAGEERROR " + e.message));
	await page.goto(BASE + "/app/home", { waitUntil: "networkidle0", timeout: 180000 });
	report.pages.deskLanding = await page.evaluate(() => ({
		href: location.href, title: document.title,
		hasFrappe: !!window.frappe, hasBoot: !!(window.frappe && window.frappe.boot),
		bodyClasses: document.body.className, bodyStart: document.body.innerText.slice(0, 300),
	}));
	fs.writeFileSync(OUT + "/ui-report.json", JSON.stringify(report, null, 2));
	await page.screenshot({ path: OUT + "/ui-desk-landing.png" });
	await page.waitForFunction(() => window.frappe && window.frappe.boot, { timeout: 90000 });
	await page.waitForFunction(() => document.querySelector(".navbar, .page-container, #body"), { timeout: 60000 }).catch(() => {});
	await new Promise((r) => setTimeout(r, 3000));
	report.pages.desk = await probe();
	report.pages.desk.bootAssets = await page.evaluate(() =>
		Object.fromEntries(Object.entries(frappe.boot.assets_json || {}).filter(([k]) => k.includes("fpm_demo")))
	);
	report.pages.desk.installedApps = await page.evaluate(() =>
		(frappe.boot.versions && Object.keys(frappe.boot.versions)) || null
	);
	await page.screenshot({ path: OUT + "/ui-desk.png" });

	// 4. The Installed Applications list, a UI a person would check.
	await page.goto(BASE + "/app/installed-applications", { waitUntil: "networkidle0", timeout: 180000 });
	await page.waitForFunction(
		() => document.body.innerText.includes("fpm_demo_base") || document.querySelector(".page-form, .form-page, .frappe-control"),
		{ timeout: 180000 }
	).catch(() => {});
	await new Promise((r) => setTimeout(r, 2000));
	await page.screenshot({ path: OUT + "/ui-installed-apps.png", fullPage: true });

	fs.writeFileSync(OUT + "/ui-report.json", JSON.stringify(report, null, 2));
	await browser.close();

	// Verdict.
	const problems = [];
	for (const p of ["login", "desk"]) {
		for (const app of APPS) {
			if (report.pages[p].markers[app] !== app.toUpperCase() + "_MARKER") problems.push(`${p}: ${app} JS marker missing`);
			if (!report.pages[p].banners[app]) problems.push(`${p}: ${app} banner not rendered`);
			if (report.pages[p].css[app].position !== "sticky") problems.push(`${p}: ${app} CSS not applied`);
		}
	}
	const bad = report.assetResponses.filter((r) => r.status !== 200);
	if (bad.length) problems.push("non-200 asset responses: " + JSON.stringify(bad));
	if (problems.length) {
		console.error("UI CHECK FAILED:\n" + problems.join("\n"));
		process.exit(1);
	}
	console.log("UI CHECK PASSED:", JSON.stringify(report.pages.desk, null, 2));
})().catch((e) => {
	console.error(e);
	process.exit(1);
});
