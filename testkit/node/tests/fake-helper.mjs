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
const witnessMode = "valid";
const cleanupFails = false;
let declaredLink = "link-peer";
if (witnessMode.startsWith("valid-link-")) declaredLink = "approved-connection";
let app;
let pair;
let installed;
let pairFailed = false;
let witnessCreated = false;
const witnessReceipt = (mode = witnessMode) => {
	const value = {
		kind: "sonarr-custom-filter-v1",
		objectId: 17,
		contentSha256: "a".repeat(64),
	};
	if (mode === "bad-kind") value.kind = "other";
	if (mode === "bad-id-zero") value.objectId = 0;
	if (mode === "bad-id-fraction") value.objectId = 1.5;
	if (mode === "bad-id-range") value.objectId = 2147483648;
	if (mode === "bad-hash") value.contentSha256 = "private-witness-body";
	if (mode === "extra-receipt-field") value.label = "private-witness-body";
	if (mode === "missing-receipt-field") value.contentSha256 = undefined;
	if (mode === "null-receipt") return null;
	return value;
};
const address = (id) => {
	let port = 12345;
	if (pair && id === pair.target) port = 12346;
	let path = "";
	if (witnessMode === "valid-paths") path = "/app/";
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
const reply = (req, result) => {
	const line = JSON.stringify({ version: 1, id: req.id, result });
	process.stdout.write(`${line.slice(0, 5)}`);
	setImmediate(() => process.stdout.write(`${line.slice(5)}\n`));
};

input.on("line", (line) => {
	const req = JSON.parse(line);
	event(req.method);
	if (req.method === "capabilities") {
		if (witnessMode === "slow-capabilities") return;
		if (witnessMode === "old-helper") {
			process.stdout.write(
				`${JSON.stringify({ version: 1, id: req.id, error: { code: "PROTOCOL", message: "private-witness-body" } })}\n`,
			);
			return;
		}
		if (witnessMode === "wrong-capability-envelope") {
			process.stdout.write(
				`${JSON.stringify({ version: 2, id: req.id, result: { sonarrFilterWitness: 1 } })}\n`,
			);
			return;
		}
		if (witnessMode === "missing-capability") return reply(req, {});
		if (witnessMode === "false-capability")
			return reply(req, { sonarrFilterWitness: false });
		if (witnessMode === "string-capability")
			return reply(req, { sonarrFilterWitness: "1" });
		if (witnessMode === "wrong-capability-revision")
			return reply(req, { sonarrFilterWitness: 2 });
		if (witnessMode === "null-capabilities") return reply(req, null);
		reply(req, { sonarrFilterWitness: 1 });
	} else if (req.method === "spinup") {
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
		if (witnessMode === "bad-initial-address")
			initial.apps[1].url = "http://127.0.0.1:33333";
		const invalidLinks = {
			"bad-link-missing": undefined,
			"bad-link-null": null,
			"bad-link-number": 17,
			"bad-link-empty": "",
			"bad-link-syntax": "not a link",
			"bad-link-long": "a".repeat(65),
		};
		if (Object.hasOwn(invalidLinks, witnessMode))
			initial.link = invalidLinks[witnessMode];
		installed = new Set([pair.consumer, pair.target]);
		reply(req, {
			consumer: { id: pair.consumer, url: address(pair.consumer) },
			target: { id: pair.target, url: address(pair.target) },
			initial,
			sonarrFilterWitness:
				pair.sonarrFilterWitness && witnessMode !== "missing-echo"
					? 1
					: undefined,
		});
	} else if (pairFailed && req.method !== "close") {
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, error: { code: "STATE" } })}\n`,
		);
	} else if (
		["createSonarrFilterWitness", "readSonarrFilterWitness"].includes(
			req.method,
		)
	) {
		if (
			!pair?.sonarrFilterWitness ||
			(req.method === "createSonarrFilterWitness" && witnessCreated) ||
			(req.method === "readSonarrFilterWitness" && !witnessCreated)
		) {
			process.stdout.write(
				`${JSON.stringify({ version: 1, id: req.id, error: { code: "STATE" } })}\n`,
			);
			return;
		}
		if (
			witnessMode === "operation-failure" ||
			witnessMode === "operation-cleanup"
		) {
			pairFailed = true;
			event("owned-cleanup");
			if (witnessMode === "operation-cleanup")
				writeFileSync(`${manifest}.${owner}.failed`, "failed", { mode: 0o600 });
			process.stdout.write(
				`${JSON.stringify({ version: 1, id: req.id, error: { code: "OPERATION", reason: "SETUP", cleanupFailed: witnessMode === "operation-cleanup", message: "private-witness-body" } })}\n`,
			);
			return;
		}
		if (req.method === "createSonarrFilterWitness") witnessCreated = true;
		let mode = witnessMode;
		if (mode.startsWith("read-")) {
			mode = mode.slice(5);
			if (req.method === "createSonarrFilterWitness") mode = "valid";
		}
		reply(req, witnessReceipt(mode));
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
		if (
			pair.consumer === "bad-inspection" ||
			witnessMode === `bad-${req.method}`
		)
			result.outcome = "imaginary";
		if (witnessMode === `bad-${req.method}-address`)
			result.apps[1].url = "http://127.0.0.1:33333";
		if (witnessMode.startsWith(`replace-${req.method}-`)) {
			result.link = "check";
			if (witnessMode.endsWith("-forged")) {
				result.outcome = "complete";
				result.reason = "verified";
				for (const item of result.apps) {
					item.installed = true;
					item.state = "running";
					item.setup = "ok";
				}
				result.apps[0].steps = [{ id: "check", status: "ok" }];
			}
			if (witnessMode.endsWith("-manual")) {
				result.outcome = "manual";
				result.reason = "setup";
				result.apps[0].setup = "manual";
				result.apps[0].steps = [{ id: declaredLink, status: "manual" }];
			}
		}
		if (
			witnessMode.startsWith("valid-link-") &&
			result.outcome === "complete"
		) {
			if (witnessMode === "valid-link-missing") {
				result.outcome = "prerequisite";
				result.reason = "not-run";
				result.apps[0].steps = [];
			} else if (witnessMode !== "valid-link-ok") {
				const status = witnessMode.slice("valid-link-".length);
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
		reply(req, result);
	} else if (pair && req.params.app && !installed.has(req.params.app)) {
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, error: { code: "STATE" } })}\n`,
		);
	} else if (witnessMode === `bad-view-${req.method}-null`) {
		reply(req, null);
	} else if (witnessMode === `bad-view-${req.method}-shape`) {
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
		witnessMode.startsWith("credential-")
	) {
		const error = {
			code: "OPERATION",
			reason: "STORAGE",
			cleanupFailed: witnessMode === "credential-cleanup",
			message: "private-test-password",
		};
		if (witnessMode !== "credential-missing") {
			pairFailed = true;
			event("owned-cleanup");
			if (error.cleanupFailed)
				writeFileSync(`${manifest}.${owner}.failed`, "failed", { mode: 0o600 });
		}
		if (witnessMode === "credential-timeout") error.code = "TIMEOUT";
		if (witnessMode === "credential-cancel") error.code = "CANCELED";
		if (witnessMode === "credential-missing") error.code = "CREDENTIALS";
		if (witnessMode === "credential-missing" && req.method === "info")
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
		if (witnessMode === "info-id") result.id = "outside";
		if (witnessMode === "info-sibling-id") result.id = sibling;
		const urls = {
			"info-remote": "http://example.invalid:12345",
			"info-https": result.url.replace("http:", "https:"),
			"info-invalid": "not a URL",
			"info-port": "http://127.0.0.1:33333",
			"info-query": `${result.url}?outside=1`,
			"info-path": `${result.url}/outside`,
		};
		if (urls[witnessMode]) result.url = urls[witnessMode];
		if (witnessMode === "info-sibling-url") result.url = address(sibling);
		reply(req, result);
	} else if (req.method === "close") {
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, result: { closed: true } })}\n`,
			() => process.exit(0),
		);
	}
});

input.on("close", () => process.exit(0));
