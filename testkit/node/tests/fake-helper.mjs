import { appendFileSync, existsSync, writeFileSync } from "node:fs";
import { createInterface } from "node:readline";

const args = process.argv.slice(2);
const argument = (name) => args[args.indexOf(name) + 1];
const owner = argument("--owner");
const cleanup = args.includes("--cleanup-manifest");
const manifest = argument(cleanup ? "--cleanup-manifest" : "--manifest");
const event = (method) =>
	appendFileSync(
		manifest,
		`${JSON.stringify({ method, owner, pid: process.pid })}\n`,
		{ mode: 0o600 },
	);

if (cleanup) {
	event("cleanup");
	event("cleanup-complete");
	process.exit(existsSync(`${manifest}.${owner}.failed`) ? 1 : 0);
}

const input = createInterface({ input: process.stdin });
const mode = "valid";
const cleanupFails = false;
let declaredLink = "link-peer";
if (mode.startsWith("valid-link-")) declaredLink = "approved-connection";
let app;
let pair;
let installed;
let pairFailed = false;
const address = (id) => {
	let port = 12345;
	if (pair && id === pair.target) port = 12346;
	let path = "";
	if (mode === "valid-paths") path = "/app/";
	return `http://127.0.0.1:${port}${path}`;
};
const inspection = () => ({
	outcome: installed.size === 2 ? "complete" : "prerequisite",
	reason: installed.size === 2 ? "verified" : "absent",
	link: declaredLink,
	apps: [pair.consumer, pair.target].map((id, index) => ({
		id,
		url: address(id),
		installed: installed.has(id),
		state: installed.has(id) ? "running" : "missing",
		setup: installed.has(id) ? "ok" : "pending",
		steps:
			index === 0 && installed.size === 2
				? [{ id: declaredLink, status: "ok" }]
				: [],
	})),
});
const integrity = (value) => {
	const kind = mode.replace(/^integrity-(?:startup|live)-/, "");
	value.outcome = "complete";
	value.reason = "verified";
	for (const item of value.apps) {
		item.installed = true;
		item.state = "running";
		item.setup = "ok";
		item.steps = [];
	}
	const consumer = value.apps[0];
	const peer = value.apps[1];
	consumer.steps = [{ id: declaredLink, status: "ok" }];
	if (kind.startsWith("fail-")) {
		let failed = consumer;
		let other = peer;
		if (kind.startsWith("fail-peer-")) [failed, other] = [peer, consumer];
		failed.steps.push({ id: "observation", status: "fail" });
		value.outcome = kind.split("-").at(-1);
		value.reason = "setup";
		if (value.outcome === "complete") value.reason = "verified";
		if (value.outcome === "manual") other.setup = "manual";
		if (value.outcome === "prerequisite") {
			other.installed = false;
			other.setup = "pending";
			other.state = "missing";
			value.reason = "absent";
		}
	} else if (kind.startsWith("duplicate-link-")) {
		consumer.steps = kind
			.slice("duplicate-link-".length)
			.split("-")
			.map((status) => ({ id: declaredLink, status }));
	} else if (kind.startsWith("duplicate-")) {
		let selected = consumer;
		if (kind === "duplicate-peer") selected = peer;
		selected.steps.push({ id: "observation", status: "ok" });
		selected.steps.push({ id: "observation", status: "ok" });
		if (kind === "duplicate-manual") {
			consumer.setup = "manual";
			value.outcome = "manual";
			value.reason = "setup";
		}
		if (kind === "duplicate-failed") {
			selected.steps.at(-1).status = "fail";
			value.outcome = "failed";
			value.reason = "setup";
		}
		if (kind === "duplicate-prerequisite") {
			peer.installed = false;
			peer.setup = "pending";
			value.outcome = "prerequisite";
			value.reason = "absent";
		}
	} else if (kind === "failed-with-manual") {
		consumer.steps.push({ id: "observation", status: "fail" });
		peer.setup = "manual";
		peer.steps = [{ id: "observation", status: "manual" }];
		value.outcome = "failed";
		value.reason = "setup";
	} else if (kind === "manual-missing") {
		consumer.setup = "manual";
		consumer.steps = [{ id: declaredLink, status: "manual" }];
		peer.installed = false;
		peer.setup = "pending";
		peer.state = "missing";
		value.outcome = "manual";
		value.reason = "setup";
	} else if (
		kind === "failed-without-fail" ||
		kind === "manual-without-manual"
	) {
		value.outcome = kind.split("-")[0];
		value.reason = "setup";
	} else if (kind.startsWith("prerequisite-")) {
		value.outcome = "prerequisite";
		value.reason = "not-run";
		if (kind !== "prerequisite-verified") consumer.steps = [];
		if (kind === "prerequisite-manual") {
			consumer.setup = "manual";
			value.reason = "unready";
		}
		if (kind === "prerequisite-absent-present") value.reason = "absent";
		if (kind === "prerequisite-missing-notrun") {
			peer.installed = false;
			peer.setup = "pending";
		}
		if (kind === "prerequisite-ready-unready") value.reason = "unready";
		if (kind === "prerequisite-pending-notrun") consumer.setup = "pending";
	} else if (kind === "verified-skip") {
		consumer.steps = [{ id: declaredLink, status: "skip", verified: true }];
	} else if (kind === "unverified-skip") {
		consumer.steps = [{ id: declaredLink, status: "skip" }];
	} else if (kind === "unique-per-app") {
		consumer.steps.push({ id: "observation", status: "ok" });
		peer.steps.push({ id: "observation", status: "ok" });
	}
	return value;
};
const reply = (req, result) => {
	const line = JSON.stringify({ version: 1, id: req.id, result });
	process.stdout.write(`${line.slice(0, 5)}`);
	setImmediate(() => process.stdout.write(`${line.slice(5)}\n`));
};

input.on("line", (line) => {
	const req = JSON.parse(line);
	event(req.method);
	if (req.method === "spinup") {
		app = req.params.app;
		if (app === "exit") process.exit(1);
		if (app === "slow") return;
		if (app === "malformed") {
			process.stdout.write("private-test-raw-secret\n");
			return;
		}
		if (app === "wrong-version") {
			process.stdout.write(
				`${JSON.stringify({ version: 2, id: req.id, result: { url: "private-test-raw-secret" } })}\n`,
			);
			return;
		}
		if (app === "oversized") {
			process.stdout.write("x".repeat(100000));
			return;
		}
		if (app === "secret-error") {
			process.stderr.write("private-test-raw-secret");
			process.stdout.write(
				`${JSON.stringify({ version: 1, id: req.id, error: { code: "START", reason: "SETUP", message: "private-test-raw-secret" } })}\n`,
			);
			return;
		}
		if (app === "cleanup-failure") {
			writeFileSync(`${manifest}.${owner}.failed`, "failed", { mode: 0o600 });
		}
		reply(req, { url: "http://127.0.0.1:12345" });
	} else if (req.method === "spinupPair") {
		pair = req.params;
		if (pair.consumer === "cleanup-failure" || cleanupFails)
			writeFileSync(`${manifest}.${owner}.failed`, "failed", { mode: 0o600 });
		if (pair.consumer === "slow") return;
		installed = new Set([pair.consumer]);
		if (pair.order === "target-first") installed = new Set([pair.target]);
		const initial = inspection();
		if (mode === "bad-initial-address")
			initial.apps[1].url = "http://127.0.0.1:33333";
		const invalidLinks = {
			"bad-link-missing": undefined,
			"bad-link-null": null,
			"bad-link-number": 17,
			"bad-link-empty": "",
			"bad-link-syntax": "not a link",
			"bad-link-long": "a".repeat(65),
		};
		if (Object.hasOwn(invalidLinks, mode)) initial.link = invalidLinks[mode];
		if (mode.startsWith("integrity-startup-")) integrity(initial);
		installed = new Set([pair.consumer, pair.target]);
		reply(req, {
			consumer: { id: pair.consumer, url: address(pair.consumer) },
			target: { id: pair.target, url: address(pair.target) },
			initial,
		});
	} else if (pairFailed && req.method !== "close") {
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, error: { code: "STATE" } })}\n`,
		);
	} else if (["inspect", "setup", "remove", "install"].includes(req.method)) {
		if (req.method === "install" && pair.consumer.startsWith("action-")) {
			pairFailed = true;
			event("owned-cleanup");
			const error = {
				code: "OPERATION",
				reason: "LIMITS",
				cleanupFailed: false,
				message: "private-test-raw-secret",
			};
			if (
				pair.consumer === "action-timeout" ||
				pair.consumer === "action-cancel"
			) {
				error.code = "CANCELED";
				if (pair.consumer === "action-timeout") error.code = "TIMEOUT";
				error.reason = undefined;
			}
			if (pair.consumer === "action-cleanup") {
				error.cleanupFailed = true;
				writeFileSync(`${manifest}.${owner}.failed`, "failed", { mode: 0o600 });
			}
			process.stdout.write(
				`${JSON.stringify({ version: 1, id: req.id, error })}\n`,
			);
			return;
		}
		if (req.method === "remove") installed.delete(req.params.app);
		if (req.method === "install") installed.add(req.params.app);
		const result = inspection();
		if (pair.consumer === "bad-inspection" || mode === `bad-${req.method}`)
			result.outcome = "imaginary";
		if (mode === `bad-${req.method}-address`)
			result.apps[1].url = "http://127.0.0.1:33333";
		if (mode.startsWith(`replace-${req.method}-`)) {
			result.link = "check";
			if (mode.endsWith("-forged")) {
				result.outcome = "complete";
				result.reason = "verified";
				for (const item of result.apps) {
					item.installed = true;
					item.state = "running";
					item.setup = "ok";
				}
				result.apps[0].steps = [{ id: "check", status: "ok" }];
			}
			if (mode.endsWith("-manual")) {
				result.outcome = "manual";
				result.reason = "setup";
				result.apps[0].setup = "manual";
				result.apps[0].steps = [{ id: declaredLink, status: "manual" }];
			}
		}
		if (mode.startsWith("valid-link-") && result.outcome === "complete") {
			if (mode === "valid-link-missing") {
				result.outcome = "prerequisite";
				result.reason = "not-run";
				result.apps[0].steps = [];
			} else if (mode !== "valid-link-ok") {
				const status = mode.slice("valid-link-".length);
				result.apps[0].steps = [{ id: declaredLink, status }];
				result.apps[0].setup = "incomplete";
				result.outcome = "failed";
				result.reason = "setup";
				if (status === "manual") {
					result.apps[0].setup = "manual";
					result.outcome = "manual";
				}
				if (status === "skip") {
					result.outcome = "prerequisite";
					result.reason = "unready";
				}
			}
		}
		if (mode.startsWith("integrity-live-")) integrity(result);
		reply(req, result);
	} else if (pair && req.params.app && !installed.has(req.params.app)) {
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, error: { code: "STATE" } })}\n`,
		);
	} else if (mode === `bad-view-${req.method}-null`) {
		reply(req, null);
	} else if (mode === `bad-view-${req.method}-shape`) {
		reply(req, {
			apiKey: 17,
			username: "admin",
			password: "private-test-password",
			email: "test@example.invalid",
			id: req.params.app ?? app,
			url: "http://127.0.0.1:12345",
			image: "peer:latest",
			imageId: "sha256:test",
			repoDigests: [17],
		});
	} else if (
		pair &&
		["getApiKey", "credentials", "info"].includes(req.method) &&
		mode.startsWith("credential-")
	) {
		const error = {
			code: "OPERATION",
			reason: "STORAGE",
			cleanupFailed: mode === "credential-cleanup",
			message: "private-test-password",
		};
		if (mode !== "credential-missing") {
			pairFailed = true;
			event("owned-cleanup");
			if (error.cleanupFailed)
				writeFileSync(`${manifest}.${owner}.failed`, "failed", { mode: 0o600 });
		}
		if (mode === "credential-timeout") error.code = "TIMEOUT";
		if (mode === "credential-cancel") error.code = "CANCELED";
		if (mode === "credential-missing") error.code = "CREDENTIALS";
		if (mode === "credential-missing" && req.method === "info")
			error.code = "INFO";
		if (error.code !== "OPERATION") error.reason = undefined;
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, error })}\n`,
		);
	} else if (req.method === "credentials") {
		reply(req, {
			username: "admin",
			password: "private-test-password",
			email: "test@example.invalid",
			apiKey: "private-test-api-key",
		});
	} else if (req.method === "getApiKey") {
		reply(req, { apiKey: "private-test-api-key" });
	} else if (req.method === "info") {
		const result = {
			id: req.params.app ?? app,
			url: address(req.params.app ?? app),
			image: "peer:latest",
			imageId: "sha256:test",
			repoDigests: ["peer@sha256:test"],
		};
		let sibling = pair?.consumer;
		if (pair && req.params.app === pair.consumer) sibling = pair.target;
		if (mode === "info-id") result.id = "outside";
		if (mode === "info-sibling-id") result.id = sibling;
		const urls = {
			"info-remote": "http://example.invalid:12345",
			"info-https": result.url.replace("http:", "https:"),
			"info-invalid": "not a URL",
			"info-port": "http://127.0.0.1:33333",
			"info-query": `${result.url}?outside=1`,
			"info-path": `${result.url}/outside`,
		};
		if (urls[mode]) result.url = urls[mode];
		if (mode === "info-sibling-url") result.url = address(sibling);
		reply(req, result);
	} else if (req.method === "close") {
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, result: { closed: true } })}\n`,
			() => process.exit(0),
		);
	}
});

input.on("close", () => process.exit(0));
