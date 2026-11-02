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
	process.exit(existsSync(`${manifest}.${owner}.failed`) ? 1 : 0);
}

const input = createInterface({ input: process.stdin });
let app;
let pair;
let installed;
const inspection = () => ({
	outcome: installed.size === 2 ? "complete" : "prerequisite",
	reason: installed.size === 2 ? "verified" : "absent",
	link: "link-peer",
	apps: [pair.consumer, pair.target].map((id, index) => ({
		id,
		url: `http://127.0.0.1:${12345 + index}`,
		installed: installed.has(id),
		state: installed.has(id) ? "running" : "missing",
		setup: installed.has(id) ? "ok" : "pending",
		steps:
			index === 0 && installed.size === 2
				? [{ id: "link-peer", status: "ok" }]
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
		if (pair.consumer === "slow") return;
		if (pair.consumer === "cleanup-failure")
			writeFileSync(`${manifest}.${owner}.failed`, "failed", { mode: 0o600 });
		installed = new Set([pair.consumer]);
		if (pair.order === "target-first") installed = new Set([pair.target]);
		const initial = inspection();
		installed = new Set([pair.consumer, pair.target]);
		reply(req, {
			consumer: { id: pair.consumer, url: "http://127.0.0.1:12345" },
			target: { id: pair.target, url: "http://127.0.0.1:12346" },
			initial,
		});
	} else if (["inspect", "setup", "remove", "install"].includes(req.method)) {
		if (req.method === "remove") installed.delete(req.params.app);
		if (req.method === "install") installed.add(req.params.app);
		const result = inspection();
		if (pair.consumer === "bad-inspection") result.outcome = "imaginary";
		reply(req, result);
	} else if (pair && req.params.app && !installed.has(req.params.app)) {
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, error: { code: "STATE" } })}\n`,
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
		reply(req, {
			id: req.params.app ?? app,
			url: "http://127.0.0.1:12345",
			image: "peer:latest",
			imageId: "sha256:test",
			repoDigests: ["peer@sha256:test"],
		});
	} else if (req.method === "close") {
		process.stdout.write(
			`${JSON.stringify({ version: 1, id: req.id, result: { closed: true } })}\n`,
			() => process.exit(0),
		);
	}
});

input.on("close", () => process.exit(0));
