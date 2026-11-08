import { type ChildProcess, spawn } from "node:child_process";
import { appendFileSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
	type App,
	type SelfHostOptions,
	TestkitError,
	createSelfHost,
} from "./index.mjs";

export interface RunApp {
	app: string;
	image?: string;
	/** Map fixture fields to environment variables already used by your tests. */
	env?: Partial<
		Record<"url" | "apiKey" | "username" | "password" | "email", string>
	>;
}

export interface RunConfig {
	apps: Record<string, string | RunApp>;
	/** Limits simultaneous startups, not the total number of running Apps. */
	concurrency?: number;
	/** Whole startup + test command budget. Cleanup has a separate reserve. */
	timeoutMs?: number;
}

export interface RunReport {
	schemaVersion: 1;
	startedAt: string;
	durationMs: number;
	apps: Array<{
		name: string;
		app: string;
		status: "pending" | "ready" | "failed" | "skipped";
		durationMs: number;
		code?: string;
		reason?: string;
		image?: string;
		imageId?: string;
		repoDigests?: string[];
	}>;
	tests: { status: "not-run" | "passed" | "failed"; exitCode: number | null };
	cleanup: "passed" | "failed";
	interrupted: boolean;
	exitCode: number;
}

export class RunConfigError extends Error {
	constructor() {
		super(
			"Invalid testkit config. Declare 1–8 named Apps, unique safe environment names, concurrency 1–8 and timeoutMs 1–780000.",
		);
	}
}

const fields = ["url", "apiKey", "username", "password", "email"] as const;
const suffixes = ["URL", "API_KEY", "USERNAME", "PASSWORD", "EMAIL"] as const;
const identifier = /^[a-z0-9][a-z0-9-]{0,63}$/;
const envName = /^[A-Z][A-Z0-9_]*$/;
const reserved =
	/^(?:PATH|HOME|SHELL|BASH_ENV|ENV|NODE_OPTIONS|NODE_PATH|LD_.*|DYLD_.*|GITHUB_.*|RUNNER_.*|SELF_HOST_TESTKIT_.*)$/;
function object(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function parseRunConfig(input: unknown): Required<RunConfig> {
	if (
		!object(input) ||
		!object(input.apps) ||
		Object.keys(input).some(
			(key) => !["apps", "concurrency", "timeoutMs"].includes(key),
		)
	)
		throw new RunConfigError();
	const entries = Object.entries(input.apps);
	const concurrency = input.concurrency ?? entries.length;
	const timeoutMs = input.timeoutMs ?? 720000;
	if (
		entries.length < 1 ||
		entries.length > 8 ||
		!Number.isSafeInteger(concurrency) ||
		Number(concurrency) < 1 ||
		Number(concurrency) > 8 ||
		!Number.isSafeInteger(timeoutMs) ||
		Number(timeoutMs) < 1 ||
		Number(timeoutMs) > 780000
	)
		throw new RunConfigError();
	const apps: Record<string, RunApp> = Object.create(null);
	const names = new Set<string>();
	for (const [name, raw] of entries) {
		const app = typeof raw === "string" ? { app: raw } : raw;
		if (
			!identifier.test(name) ||
			!object(app) ||
			typeof app.app !== "string" ||
			!identifier.test(app.app) ||
			Object.keys(app).some((key) => !["app", "image", "env"].includes(key)) ||
			(app.image !== undefined &&
				(typeof app.image !== "string" || /[\0\r\n]/.test(app.image))) ||
			(app.env !== undefined && !object(app.env))
		)
			throw new RunConfigError();
		const env: RunApp["env"] = {};
		if (
			app.env &&
			Object.keys(app.env).some(
				(key) => !(fields as readonly string[]).includes(key),
			)
		)
			throw new RunConfigError();
		for (const [index, field] of fields.entries()) {
			const variable =
				app.env?.[field] ??
				`SELF_HOST_${name.replaceAll("-", "_").toUpperCase()}_${suffixes[index]}`;
			if (
				typeof variable !== "string" ||
				!envName.test(variable) ||
				reserved.test(variable) ||
				names.has(variable)
			)
				throw new RunConfigError();
			names.add(variable);
			env[field] = variable;
		}
		apps[name] = { app: app.app, image: app.image as string | undefined, env };
	}
	return {
		apps,
		concurrency: Number(concurrency),
		timeoutMs: Number(timeoutMs),
	};
}

function mask(value: string) {
	if (value && process.env.GITHUB_ACTIONS === "true") {
		process.stdout.write(
			`::add-mask::${value.replaceAll("%", "%25").replaceAll("\r", "%0D").replaceAll("\n", "%0A")}\n`,
		);
	}
}

async function environment(
	app: App,
	config: RunApp,
): Promise<NodeJS.ProcessEnv> {
	const credentials = await app.credentials();
	const values = {
		url: app.url,
		apiKey: credentials.apiKey,
		username: credentials.username,
		password: credentials.password,
		email: credentials.email,
	};
	const result: NodeJS.ProcessEnv = {};
	for (const field of fields) {
		if (field !== "url") mask(values[field]);
		const name = config.env?.[field];
		if (name) result[name] = values[field];
	}
	return result;
}

function killTree(child: ChildProcess, signal: NodeJS.Signals) {
	if (!child.pid) return;
	try {
		if (process.platform !== "win32") process.kill(-child.pid, signal);
		else child.kill(signal);
	} catch {
		/* Already exited. */
	}
}

function command(
	command: readonly string[],
	env: NodeJS.ProcessEnv,
	cwd: string | undefined,
	signal: AbortSignal,
): Promise<number | null> {
	return new Promise((resolve) => {
		if (signal.aborted) return resolve(null);
		const child = spawn(command[0] as string, command.slice(1), {
			cwd,
			env,
			stdio: "inherit",
			detached: process.platform !== "win32",
			shell: false,
		});
		let timer: ReturnType<typeof setTimeout> | undefined;
		const abort = () => {
			killTree(child, "SIGTERM");
			timer ??= setTimeout(() => killTree(child, "SIGKILL"), 5000);
		};
		signal.addEventListener("abort", abort, { once: true });
		if (signal.aborted) abort();
		child.once("error", () => resolve(null));
		child.once("close", (code) => {
			if (timer) clearTimeout(timer);
			signal.removeEventListener("abort", abort);
			// Do not leave descendants holding the now-stale fixture environment.
			killTree(child, "SIGKILL");
			resolve(code);
		});
	});
}

/** Starts fixtures together, runs an existing command, and always attempts owned cleanup. */
export async function runTests(
	config: RunConfig,
	testCommand: readonly string[],
	options: SelfHostOptions & { cwd?: string; signal?: AbortSignal } = {},
): Promise<RunReport> {
	const parsed = parseRunConfig(config);
	if (
		!testCommand.length ||
		testCommand.some((part) => typeof part !== "string" || part.includes("\0"))
	)
		throw new RunConfigError();
	const started = Date.now();
	const apps = Object.entries(parsed.apps) as Array<[string, RunApp]>;
	const report: RunReport = {
		schemaVersion: 1,
		startedAt: new Date(started).toISOString(),
		durationMs: 0,
		apps: apps.map(([name, app]) => ({
			name,
			app: app.app,
			status: "pending",
			durationMs: 0,
		})),
		tests: { status: "not-run", exitCode: null },
		cleanup: "passed",
		interrupted: false,
		exitCode: 1,
	};
	const controller = new AbortController();
	const abort = () => controller.abort();
	const timer = setTimeout(abort, parsed.timeoutMs);
	options.signal?.addEventListener("abort", abort, { once: true });
	if (options.signal?.aborted) abort();
	let host: ReturnType<typeof createSelfHost> | undefined;
	const env = { ...process.env };
	let cursor = 0;
	const stop = () => {
		void host?.close().catch(() => {});
	};
	controller.signal.addEventListener("abort", stop, { once: true });
	try {
		const activeHost = createSelfHost({
			...options,
			timeoutMs: parsed.timeoutMs + 60000,
		});
		host = activeHost;
		if (controller.signal.aborted) stop();
		const startNext = async () => {
			while (!controller.signal.aborted) {
				const index = cursor++;
				const entry = apps[index];
				const result = report.apps[index];
				if (!entry || !result) return;
				const began = Date.now();
				try {
					const app = await activeHost.spinup(entry[1].app, {
						image: entry[1].image,
					});
					Object.assign(env, await environment(app, entry[1]));
					const info = await app.info();
					if (
						!/^[a-z0-9][a-zA-Z0-9./:_@-]{0,300}$/.test(info.image) ||
						!/^sha256:[a-f0-9]{64}$/.test(info.imageId) ||
						!info.repoDigests.every((digest) =>
							/^[a-z0-9][a-zA-Z0-9./:_-]{0,240}@sha256:[a-f0-9]{64}$/.test(
								digest,
							),
						)
					) {
						throw new TestkitError("INFO");
					}
					result.image = info.image;
					result.imageId = info.imageId;
					result.repoDigests = info.repoDigests;
					result.status = "ready";
				} catch (error) {
					result.status = "failed";
					if (error instanceof TestkitError) {
						result.code = error.code;
						result.reason = error.reason;
					}
				} finally {
					result.durationMs = Date.now() - began;
				}
			}
		};
		await Promise.all(
			Array.from(
				{ length: Math.min(parsed.concurrency, apps.length) },
				startNext,
			),
		);
		if (
			!controller.signal.aborted &&
			report.apps.every((app) => app.status === "ready")
		) {
			report.tests.exitCode = await command(
				testCommand,
				env,
				options.cwd,
				controller.signal,
			);
			report.tests.status =
				report.tests.exitCode === 0 && !controller.signal.aborted
					? "passed"
					: "failed";
		}
	} catch (error) {
		for (const app of report.apps) {
			if (app.status === "pending") {
				app.status = "failed";
				if (error instanceof TestkitError) {
					app.code = error.code;
					app.reason = error.reason;
				}
			}
		}
	} finally {
		clearTimeout(timer);
		controller.signal.removeEventListener("abort", stop);
		try {
			await host?.close();
		} catch {
			report.cleanup = "failed";
		}
		options.signal?.removeEventListener("abort", abort);
		report.interrupted = controller.signal.aborted;
		for (const app of report.apps)
			if (app.status === "pending") app.status = "skipped";
		report.durationMs = Date.now() - started;
		report.exitCode =
			report.tests.status === "passed" &&
			report.cleanup === "passed" &&
			!report.interrupted
				? 0
				: report.tests.exitCode || 1;
	}
	return report;
}

export function markdownReport(report: RunReport): string {
	return [
		"## self-host testkit",
		"",
		`Test command: **${report.tests.status}**. Cleanup: **${report.cleanup}**. Interrupted: **${report.interrupted}**.`,
		"",
		"| Fixture | App | Setup | Seconds | Reason |",
		"| --- | --- | --- | ---: | --- |",
		...report.apps.map(
			(app) =>
				`| ${app.name} | ${app.app} | ${app.status} | ${(app.durationMs / 1000).toFixed(1)} | ${app.code ?? ""}${app.reason ? ` / ${app.reason}` : ""} |`,
		),
		"",
		"Setup readiness is not an adapter assertion. Only the existing test command determines its test result.",
		"",
	].join("\n");
}

export function writeRunReport(report: RunReport, directory: string): void {
	mkdirSync(directory, { recursive: true });
	writeFileSync(
		join(directory, "report.json"),
		`${JSON.stringify(report, null, 2)}\n`,
	);
	const markdown = markdownReport(report);
	writeFileSync(join(directory, "report.md"), markdown);
	if (process.env.GITHUB_STEP_SUMMARY)
		appendFileSync(process.env.GITHUB_STEP_SUMMARY, markdown);
}
