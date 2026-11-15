import { afterEach, expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { markdownReport } from "../run.mjs";

const directories: string[] = [];
function fixture() {
	const directory = mkdtempSync(join(tmpdir(), "runner-ci-test-"));
	directories.push(directory);
	cpSync(new URL("../examples/runner-ci/", import.meta.url), directory, {
		recursive: true,
	});
	const bin = join(directory, "bin");
	mkdirSync(bin);
	writeFileSync(
		join(bin, "docker"),
		'#!/bin/sh\nprintf "%s" "${FAKE_REMAINING_RESOURCE-}"\n',
		{ mode: 0o700 },
	);
	const imageId = `sha256:${"a".repeat(64)}`;
	writeFileSync(
		join(directory, "witness.json"),
		JSON.stringify({ project: `testkit-${"b".repeat(24)}`, imageId }),
	);
	const report = {
		schemaVersion: 1 as const,
		startedAt: new Date().toISOString(),
		durationMs: 200,
		apps: [
			{
				name: "sonarr",
				app: "sonarr",
				status: "ready" as const,
				durationMs: 100,
				image: "lscr.io/linuxserver/sonarr:latest",
				imageId,
				repoDigests: [`lscr.io/linuxserver/sonarr@${imageId}`],
			},
		],
		tests: { status: "passed" as "passed" | "failed", exitCode: 0 },
		cleanup: "passed" as const,
		interrupted: false,
		exitCode: 0,
	};
	const env = {
		...process.env,
		PATH: `${bin}:${process.env.PATH}`,
		SONARR_URL: undefined,
		SONARR_API_KEY: undefined,
	};
	function verify(extra: NodeJS.ProcessEnv = {}) {
		const reports = join(directory, "self-host-testkit-report");
		mkdirSync(reports, { recursive: true });
		writeFileSync(join(reports, "report.json"), JSON.stringify(report));
		writeFileSync(join(reports, "report.md"), markdownReport(report));
		return spawnSync("node", ["verify.mjs"], {
			cwd: directory,
			env: { ...env, ...extra },
			stdio: "pipe",
			timeout: 10_000,
		}).status;
	}
	return { directory, env, report, verify };
}

afterEach(() => {
	for (const directory of directories.splice(0))
		rmSync(directory, { recursive: true, force: true });
});

test("hosted verifier accepts matching provenance and empty owned resources", () => {
	expect(fixture().verify()).toBe(0);
});

test("hosted verifier rejects remaining resources before post cleanup", () => {
	expect(fixture().verify({ FAKE_REMAINING_RESOURCE: "abc123" })).not.toBe(0);
});

test("hosted verifier rejects failed tests, mismatched image and leaked environment", () => {
	const f = fixture();
	f.report.tests = { status: "failed", exitCode: 23 };
	expect(f.verify()).not.toBe(0);
	f.report.tests = { status: "passed", exitCode: 0 };
	f.report.apps[0].imageId = `sha256:${"c".repeat(64)}`;
	expect(f.verify()).not.toBe(0);
	f.report.apps[0].imageId = `sha256:${"a".repeat(64)}`;
	expect(f.verify({ SONARR_API_KEY: "synthetic-test-value" })).not.toBe(0);
});

test("ordinary Sonarr assertions fail rather than skip when injection is missing", () => {
	const f = fixture();
	const result = spawnSync("node", ["--test", "sonarr.test.mjs"], {
		cwd: f.directory,
		env: f.env,
		stdio: "pipe",
		timeout: 10_000,
	});
	expect(result.status).toBe(1);
	expect(result.stdout.toString()).toContain(
		"Runner did not supply the configured environment.",
	);
});

test("public pull diagnostics classify without printing raw Docker output", () => {
	const f = fixture();
	for (const [output, code, category] of [
		[
			"private signed URL: https://invalid.example/?token=secret 429 Too Many Requests",
			1,
			"registry-rate-limit",
		],
		["403 Forbidden: private-synthetic-key", 1, "registry-authorization"],
		[
			"503 Service Unavailable private-synthetic-key",
			1,
			"registry-server-error",
		],
		["private-synthetic-key unexpected failure", 1, "unclassified-failure"],
		[
			"https://invalid.example/?token=private429403500 unexpected failure",
			1,
			"unclassified-failure",
		],
		["sha256:private-synthetic-key", 0, "passed"],
	] as const) {
		writeFileSync(
			join(f.directory, "bin", "docker"),
			`#!/bin/sh\nprintf '%s' '${output}' >&2\nexit ${code}\n`,
			{ mode: 0o700 },
		);
		const result = spawnSync("node", ["diagnose-pull.mjs"], {
			cwd: f.directory,
			env: f.env,
			encoding: "utf8",
			timeout: 10_000,
		});
		expect(result.status).toBe(0);
		expect(result.stdout).toContain(
			`Separate public Sonarr pull probe: ${category}.`,
		);
		expect(result.stdout).not.toContain("private");
		expect(result.stdout).not.toContain("token=");
		expect(result.stderr).toBe("");
	}
});
