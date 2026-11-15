import { afterEach, expect, test } from "bun:test";
import { spawn } from "node:child_process";
import {
	chmodSync,
	cpSync,
	mkdirSync,
	mkdtempSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const directories: string[] = [];
function fixture(run = true) {
	const directory = mkdtempSync(join(tmpdir(), "testkit-action-"));
	directories.push(directory);
	const consumer = join(directory, "consumer");
	const sdk = join(consumer, "node_modules/@ajnart/self-host-testkit");
	mkdirSync(sdk, { recursive: true });
	const root = resolve(import.meta.dir, "..");
	for (const file of [
		"package.json",
		"index.mjs",
		"binary.mjs",
		"reasons.mjs",
		"run.mjs",
		"cli.mjs",
	])
		cpSync(join(root, file), join(sdk, file));
	writeFileSync(join(consumer, "package.json"), '{"type":"module"}');
	const version = JSON.parse(
		readFileSync(join(root, "package.json"), "utf8"),
	).version;
	const cache = join(directory, "cache");
	const binaryDirectory = join(
		cache,
		"self-host-testkit",
		version,
		`${process.platform}-${process.arch}`,
	);
	mkdirSync(binaryDirectory, { recursive: true, mode: 0o700 });
	chmodSync(binaryDirectory, 0o700);
	const helper = readFileSync(
		new URL("./fake-helper.mjs", import.meta.url),
		"utf8",
	).replaceAll("sha256:test", `sha256:${"a".repeat(64)}`);
	writeFileSync(
		join(binaryDirectory, "self-host-testkit"),
		`#!/usr/bin/env node\n${helper}`,
		{ mode: 0o700 },
	);
	const env = {
		...process.env,
		XDG_CACHE_HOME: cache,
		GITHUB_WORKSPACE: consumer,
		RUNNER_TEMP: directory,
		GITHUB_STATE: join(directory, "state"),
		GITHUB_OUTPUT: join(directory, "output"),
		GITHUB_ENV: join(directory, "env"),
		GITHUB_STEP_SUMMARY: join(directory, "summary"),
		INPUT_SOURCE: "false",
		INPUT_APPS: run ? "sonarr" : "",
		INPUT_RUN: run
			? `node -e 'if (!process.env.SELF_HOST_SONARR_API_KEY) process.exit(9)'`
			: "",
		"INPUT_REPORT-DIRECTORY": "reports",
	};
	for (const file of [
		env.GITHUB_STATE,
		env.GITHUB_OUTPUT,
		env.GITHUB_ENV,
		env.GITHUB_STEP_SUMMARY,
	])
		writeFileSync(file, "");
	return { directory, consumer, env };
}
function execute(env: NodeJS.ProcessEnv) {
	return new Promise<{ status: number | null; output: string }>((done) => {
		const child = spawn("node", [resolve(import.meta.dir, "../action.mjs")], {
			env,
			stdio: "pipe",
		});
		let output = "";
		child.stdout.on("data", (chunk) => {
			output += chunk;
		});
		child.stderr.on("data", (chunk) => {
			output += chunk;
		});
		child.once("close", (status) => done({ status, output }));
	});
}
afterEach(() => {
	for (const directory of directories.splice(0))
		rmSync(directory, { recursive: true, force: true });
});

test("Action runs installed SDK, publishes safe reports and retains post cleanup", async () => {
	const f = fixture();
	const result = await execute(f.env);
	expect(result).toMatchObject({ status: 0 });
	const report = JSON.parse(
		readFileSync(join(f.consumer, "reports/report.json"), "utf8"),
	);
	expect(report.tests.status).toBe("passed");
	expect(report.cleanup).toBe("passed");
	expect(readFileSync(f.env.GITHUB_OUTPUT, "utf8")).toContain(
		`report-directory=${f.consumer}/reports`,
	);
	expect(readFileSync(f.env.GITHUB_STEP_SUMMARY, "utf8")).toContain(
		"Test command: **passed**",
	);
	const state = Object.fromEntries(
		readFileSync(f.env.GITHUB_STATE, "utf8")
			.trim()
			.split("\n")
			.map((line) => {
				const i = line.indexOf("=");
				return [`STATE_${line.slice(0, i)}`, line.slice(i + 1)];
			}),
	);
	expect((await execute({ ...f.env, ...state })).status).toBe(0);
});

test("Action setup-only mode remains compatible", async () => {
	const f = fixture(false);
	expect((await execute(f.env)).status).toBe(0);
	expect(readFileSync(f.env.GITHUB_ENV, "utf8")).toContain(
		"SELF_HOST_TESTKIT_BINARY=",
	);
	expect(readFileSync(f.env.GITHUB_OUTPUT, "utf8")).not.toContain(
		"report-directory=",
	);
});

test("Action fails when existing tests fail while still generating report", async () => {
	const f = fixture();
	f.env.INPUT_RUN = "exit 23";
	expect((await execute(f.env)).status).toBe(1);
	const report = JSON.parse(
		readFileSync(join(f.consumer, "reports/report.json"), "utf8"),
	);
	expect(report.tests).toEqual({ status: "failed", exitCode: 23 });
	expect(report.cleanup).toBe("passed");
});

test("Action rejects a failed command hidden by a successful pipeline tail", async () => {
	const f = fixture();
	f.env.INPUT_RUN = "exit 23 | cat";
	expect((await execute(f.env)).status).toBe(1);
	const report = JSON.parse(
		readFileSync(join(f.consumer, "reports/report.json"), "utf8"),
	);
	expect(report.tests).toEqual({ status: "failed", exitCode: 23 });
});

test("npm runner does not shadow the existing Go helper on PATH", () => {
	const metadata = JSON.parse(
		readFileSync(new URL("../package.json", import.meta.url), "utf8"),
	);
	expect(metadata.bin["self-host-testkit-run"]).toBe("./cli.mjs");
	expect(metadata.bin["self-host-testkit"]).toBeUndefined();
});
