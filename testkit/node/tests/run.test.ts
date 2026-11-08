import { afterEach, expect, test } from "bun:test";
import { spawn } from "node:child_process";
import {
	chmodSync,
	mkdtempSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { parseRunConfig, runTests, writeRunReport } from "../run.mjs";

const directories: string[] = [];
function fixture() {
	const directory = mkdtempSync(join(tmpdir(), "testkit-run-"));
	directories.push(directory);
	chmodSync(directory, 0o700);
	const binary = join(directory, "helper");
	const manifest = join(directory, "events.jsonl");
	const source = readFileSync(
		new URL("./fake-helper.mjs", import.meta.url),
		"utf8",
	).replaceAll("sha256:test", `sha256:${"a".repeat(64)}`);
	// A pair of independent fixtures can become ready only if both starts overlap.
	const selected = source
		.replace(
			"import { appendFileSync, existsSync, writeFileSync }",
			"import { appendFileSync, existsSync, writeFileSync, readFileSync }",
		)
		.replace(
			'reply(req, { url: "http://127.0.0.1:12345" });',
			`if (app.startsWith("parallel-")) {
		const timer = setInterval(() => {
			if (readFileSync(manifest, "utf8").split("\\n").filter(line => line.includes('"method":"spinup"')).length < 2) return;
			clearInterval(timer);
			reply(req, { url: "http://127.0.0.1:12345" });
		}, 5);
	} else reply(req, { url: "http://127.0.0.1:12345" });`,
		);
	writeFileSync(binary, `#!${process.execPath}\n${selected}`, { mode: 0o700 });
	return { directory, binary, manifest };
}
function log(manifest: string): Array<{ method: string; owner: string }> {
	return readFileSync(manifest, "utf8")
		.trim()
		.split("\n")
		.map((line) => JSON.parse(line));
}
afterEach(() => {
	for (const directory of directories.splice(0))
		rmSync(directory, { recursive: true, force: true });
});

for (const config of [
	{},
	{ apps: {} },
	{ apps: { a: "sonarr" }, concurrency: 0 },
	{ apps: { a: "sonarr" }, timeoutMs: 780001 },
	{ apps: { a: "sonarr" }, typo: true },
	{ apps: { a: { app: "sonarr", env: { password: "PATH" } } } },
	{ apps: { a: { app: "sonarr", env: { url: "NODE_OPTIONS" } } } },
	{
		apps: {
			a: { app: "sonarr", env: { url: "PEER" } },
			b: { app: "radarr", env: { url: "PEER" } },
		},
	},
	{ apps: { "bad name": "sonarr" } },
	{ apps: { a: { app: "sonarr", env: { unknown: "FIELD" } } } },
]) {
	test(`invalid config is rejected before starting fixtures: ${JSON.stringify(config)}`, () => {
		expect(() => parseRunConfig(config)).toThrow();
	});
}

test("starts independent Apps together, maps existing env and runs the command once", async () => {
	const f = fixture();
	const report = await runTests(
		{
			apps: {
				first: {
					app: "parallel-a",
					env: { url: "SONARR_URL", apiKey: "SONARR_KEY" },
				},
				second: "parallel-b",
			},
			timeoutMs: 5000,
		},
		[
			process.execPath,
			"-e",
			`if (process.env.SONARR_URL !== 'http://127.0.0.1:12345' || process.env.SONARR_KEY !== 'private-test-api-key' || !process.env.SELF_HOST_SECOND_PASSWORD) process.exit(7)`,
		],
		f,
	);
	expect(report.exitCode).toBe(0);
	expect(report.apps.map((app) => app.status)).toEqual(["ready", "ready"]);
	expect(report.tests.status).toBe("passed");
	expect(report.cleanup).toBe("passed");
	const events = log(f.manifest);
	expect(events.filter((event) => event.method === "spinup")).toHaveLength(2);
	expect(events.filter((event) => event.method === "close")).toHaveLength(2);
	writeRunReport(report, join(f.directory, "report"));
	const text =
		readFileSync(join(f.directory, "report/report.json"), "utf8") +
		readFileSync(join(f.directory, "report/report.md"), "utf8");
	for (const privateValue of [
		"private-test-api-key",
		"private-test-password",
		"127.0.0.1",
		f.manifest,
	])
		expect(text).not.toContain(privateValue);
});

test("startup concurrency one serializes starts without claiming it caps resident Apps", async () => {
	const f = fixture();
	const report = await runTests(
		{
			apps: { first: "sonarr", second: "radarr" },
			concurrency: 1,
			timeoutMs: 5000,
		},
		[process.execPath, "-e", "process.exit(0)"],
		f,
	);
	expect(report.exitCode).toBe(0);
	const events = log(f.manifest);
	expect(events.map((event) => event.method).slice(0, 6)).toEqual([
		"spinup",
		"credentials",
		"info",
		"spinup",
		"credentials",
		"info",
	]);
});

test("keeps an existing command failure and still closes every fixture", async () => {
	const f = fixture();
	const report = await runTests(
		{ apps: { peer: "sonarr" } },
		[process.execPath, "-e", "process.exit(23)"],
		f,
	);
	expect(report.exitCode).toBe(23);
	expect(report.tests.status).toBe("failed");
	expect(report.cleanup).toBe("passed");
	expect(log(f.manifest).some((event) => event.method === "close")).toBe(true);
});

test("partial startup failure is reported, all starts settle and tests do not run", async () => {
	const f = fixture();
	const report = await runTests(
		{ apps: { broken: "secret-error", healthy: "sonarr" } },
		[process.execPath, "-e", "process.exit(23)"],
		f,
	);
	expect(report.exitCode).toBe(1);
	expect(report.tests.status).toBe("not-run");
	expect(report.apps[0]).toMatchObject({
		status: "failed",
		code: "START",
		reason: "SETUP",
	});
	expect(report.apps[1]?.status).toBe("ready");
	expect(JSON.stringify(report)).not.toContain("private-test");
	expect(
		log(f.manifest).filter((event) => event.method === "close"),
	).toHaveLength(2);
});

test("a missing test command is a failure rather than a fixture pass", async () => {
	const f = fixture();
	const report = await runTests(
		{ apps: { peer: "sonarr" } },
		[join(f.directory, "absent")],
		f,
	);
	expect(report.exitCode).toBe(1);
	expect(report.tests.status).toBe("failed");
	expect(report.cleanup).toBe("passed");
});

test("whole-run timeout terminates tests and cleans up", async () => {
	const f = fixture();
	const report = await runTests(
		{ apps: { peer: "sonarr" }, timeoutMs: 500 },
		[process.execPath, "-e", "setInterval(() => {}, 1000)"],
		f,
	);
	expect(report.interrupted).toBe(true);
	expect(report.tests.status).toBe("failed");
	expect(report.exitCode).not.toBe(0);
	expect(report.cleanup).toBe("passed");
});

test("pre-aborted runs never start fixtures or tests", async () => {
	const f = fixture();
	const report = await runTests(
		{ apps: { peer: "sonarr" } },
		[process.execPath, "-e", "process.exit(0)"],
		{ ...f, signal: AbortSignal.abort() },
	);
	expect(report.interrupted).toBe(true);
	expect(report.apps[0]?.status).toBe("skipped");
	expect(report.tests.status).toBe("not-run");
});

test("CLI works without shell interpolation and writes safe reports", async () => {
	const f = fixture();
	const child = spawn(
		process.execPath,
		[
			resolve(import.meta.dir, "../cli.mjs"),
			"--apps",
			"sonarr",
			"--report-dir",
			join(f.directory, "report"),
			"--",
			process.execPath,
			"-e",
			"process.exit(process.env.SELF_HOST_SONARR_API_KEY ? 0 : 9)",
		],
		{
			env: {
				...process.env,
				SELF_HOST_TESTKIT_BINARY: f.binary,
				SELF_HOST_TESTKIT_MANIFEST: f.manifest,
			},
			stdio: "pipe",
		},
	);
	const result = await new Promise((resolve) => child.once("close", resolve));
	expect(result).toBe(0);
	expect(
		JSON.parse(readFileSync(join(f.directory, "report/report.json"), "utf8"))
			.tests.status,
	).toBe("passed");
});

test("cleanup failure fails a successful command and preserves a failed command's exit code", async () => {
	for (const expected of [0, 23]) {
		const f = fixture();
		const report = await runTests(
			{ apps: { peer: "cleanup-failure" } },
			[process.execPath, "-e", `process.exit(${expected})`],
			f,
		);
		expect(report.cleanup).toBe("failed");
		expect(report.tests.status).toBe(expected === 0 ? "passed" : "failed");
		expect(report.exitCode).toBe(expected || 1);
	}
});

test("abort during startup cancels pending work and attempts owned cleanup", async () => {
	const f = fixture();
	const controller = new AbortController();
	const pending = runTests(
		{
			apps: { peer: "slow", queued: "sonarr" },
			concurrency: 1,
			timeoutMs: 5000,
		},
		[process.execPath, "-e", "process.exit(0)"],
		{ ...f, signal: controller.signal },
	);
	setTimeout(() => controller.abort(), 100);
	const report = await pending;
	expect(report.interrupted).toBe(true);
	expect(report.tests.status).toBe("not-run");
	expect(report.apps[1]?.status).toBe("skipped");
	expect(report.cleanup).toBe("passed");
	expect(
		log(f.manifest).filter((event) => event.method === "cleanup").length,
	).toBeGreaterThan(0);
});

test("cancellation during cleanup remains a canceled run, while cleanup completes", async () => {
	const f = fixture();
	writeFileSync(
		f.binary,
		readFileSync(f.binary, "utf8").replace(
			'event("cleanup");',
			'event("cleanup"); await new Promise(resolve => setTimeout(resolve, 300));',
		),
		{ mode: 0o700 },
	);
	const controller = new AbortController();
	const timer = setInterval(() => {
		try {
			if (log(f.manifest).some((event) => event.method === "cleanup")) {
				clearInterval(timer);
				controller.abort();
			}
		} catch {
			/* Startup has not written the log yet. */
		}
	}, 5);
	try {
		const report = await runTests(
			{ apps: { peer: "sonarr" } },
			[process.execPath, "-e", "process.exit(0)"],
			{ ...f, signal: controller.signal },
		);
		expect(report.tests.status).toBe("passed");
		expect(report.cleanup).toBe("passed");
		expect(report.interrupted).toBe(true);
		expect(report.exitCode).toBe(1);
		expect(log(f.manifest).at(-1)?.method).toBe("cleanup-complete");
	} finally {
		clearInterval(timer);
	}
});
