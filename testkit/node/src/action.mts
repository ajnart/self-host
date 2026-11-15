import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
	appendFileSync,
	chmodSync,
	lstatSync,
	mkdtempSync,
	readFileSync,
	realpathSync,
} from "node:fs";
import { findPackageJSON } from "node:module";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import {
	installedBinary,
	packageVersion,
	privateDirectory,
} from "./binary.mjs";
import { SetupError, setup } from "./setup.mjs";

class ActionError extends Error {}

function path(value: string | undefined): string {
	if (!value || /[\0\r\n]/.test(value)) {
		throw new ActionError("Testkit Action requires valid, single-line paths.");
	}
	return value;
}

function absolutePath(value: string | undefined): string {
	const result = path(value);
	if (!isAbsolute(result)) {
		throw new ActionError("Testkit Action requires absolute runner paths.");
	}
	return result;
}

function identity(binary: string): string {
	if (installedBinary(dirname(binary)) !== binary) {
		throw new ActionError("Testkit cleanup helper is unavailable or unsafe.");
	}
	return createHash("sha256").update(readFileSync(binary)).digest("hex");
}

async function main(): Promise<void> {
	const state = absolutePath(process.env.GITHUB_STATE);
	appendFileSync(state, "testkit_post=true\n");
	const output = absolutePath(process.env.GITHUB_OUTPUT);
	const environment = absolutePath(process.env.GITHUB_ENV);
	const source = process.env.INPUT_SOURCE ?? "false";
	if (source !== "true" && source !== "false") {
		throw new ActionError("Testkit source input must be true or false.");
	}
	const workspace = absolutePath(process.env.GITHUB_WORKSPACE);
	const workingDirectory = realpathSync(
		resolve(
			workspace,
			path(process.env["INPUT_WORKING-DIRECTORY"] ?? workspace),
		),
	);
	if (!lstatSync(workingDirectory).isDirectory()) {
		throw new ActionError("Testkit working-directory must be a directory.");
	}
	process.umask(0o077);
	const directory = mkdtempSync(
		join(absolutePath(process.env.RUNNER_TEMP), "self-host-testkit-"),
	);
	chmodSync(directory, 0o700);
	const manifest = join(directory, "manifest.jsonl");
	let binary: string;
	let sdk: string;
	if (source === "true") {
		const root = fileURLToPath(new URL("../../", import.meta.url));
		sdk = join(root, "testkit", "node");
		const bootstrap = spawnSync(
			"python3",
			[join(root, "testkit", "setup-go.py"), directory],
			{ stdio: "ignore", timeout: 300_000, killSignal: "SIGKILL" },
		);
		if (bootstrap.status !== 0) {
			throw new ActionError("Testkit private source toolchain setup failed.");
		}
		binary = join(directory, "self-host-testkit");
		const build = spawnSync(
			join(directory, "go", "bin", "go"),
			["build", "-trimpath", "-o", binary, "./cmd/self-host-testkit"],
			{
				cwd: root,
				stdio: "ignore",
				timeout: 300_000,
				killSignal: "SIGKILL",
				env: {
					...process.env,
					CGO_ENABLED: "0",
					GOROOT: join(directory, "go"),
					GOTOOLCHAIN: "local",
				},
			},
		);
		if (build.status !== 0) {
			throw new ActionError("Testkit source helper build failed.");
		}
		chmodSync(binary, 0o700);
	} else {
		let exactVersion: string;
		try {
			const metadata = findPackageJSON(
				"@ajnart/self-host-testkit",
				pathToFileURL(join(workingDirectory, "package.json")),
			);
			if (!metadata) throw new ActionError("Missing SDK.");
			exactVersion = packageVersion(metadata);
			sdk = dirname(metadata);
		} catch {
			throw new ActionError(
				"Install @ajnart/self-host-testkit with npm ci before this Action, and check working-directory.",
			);
		}
		binary = await setup({ version: exactVersion });
	}
	path(sdk);
	path(binary);
	const checksum = identity(binary);
	appendFileSync(
		state,
		`testkit_manifest=${manifest}\ntestkit_binary=${binary}\ntestkit_checksum=${checksum}\n`,
	);
	appendFileSync(
		output,
		`binary-path=${binary}\nsdk-path=${sdk}\nmanifest-path=${manifest}\n`,
	);
	appendFileSync(
		environment,
		`SELF_HOST_TESTKIT_BINARY=${binary}\nSELF_HOST_TESTKIT_SDK=${sdk}\nSELF_HOST_TESTKIT_MANIFEST=${manifest}\n`,
	);
	const run = process.env.INPUT_RUN;
	const apps = process.env.INPUT_APPS;
	const config = process.env.INPUT_CONFIG;
	if (!run) {
		if (apps || config)
			throw new ActionError("Set run when declaring testkit Apps or config.");
		return;
	}
	if (Boolean(apps) === Boolean(config)) {
		throw new ActionError("Set exactly one of apps or config with run.");
	}
	const reportDirectory = resolve(
		workingDirectory,
		process.env["INPUT_REPORT-DIRECTORY"] || "self-host-testkit-report",
	);
	path(reportDirectory);
	appendFileSync(output, `report-directory=${reportDirectory}\n`);
	const args = [
		join(sdk, "cli.mjs"),
		apps ? "--apps" : "--config",
		apps || config || "",
		"--report-dir",
		reportDirectory,
	];
	if (process.env.INPUT_CONCURRENCY)
		args.push("--concurrency", process.env.INPUT_CONCURRENCY);
	if (process.env["INPUT_TIMEOUT-MS"])
		args.push("--timeout-ms", process.env["INPUT_TIMEOUT-MS"]);
	args.push("--", "/bin/bash", "-euo", "pipefail", "-c", run);
	try {
		lstatSync(args[0] as string);
	} catch {
		throw new ActionError(
			"This installed SDK does not include the command runner. Upgrade the SDK to a release that includes it, or use source for development.",
		);
	}
	const status = await new Promise<number | null>((resolve) => {
		const child = spawn(process.execPath, args, {
			cwd: workingDirectory,
			stdio: "inherit",
			env: {
				...process.env,
				SELF_HOST_TESTKIT_BINARY: binary,
				SELF_HOST_TESTKIT_MANIFEST: manifest,
			},
		});
		const interrupt = () => child.kill("SIGINT");
		const terminate = () => child.kill("SIGTERM");
		process.on("SIGINT", interrupt);
		process.on("SIGTERM", terminate);
		child.once("error", () => resolve(null));
		child.once("close", (code) => {
			process.removeListener("SIGINT", interrupt);
			process.removeListener("SIGTERM", terminate);
			resolve(code);
		});
	});
	if (status !== 0)
		throw new ActionError(
			"Testkit run failed. Check the sanitized report and the test command output.",
		);
}

function post(): void {
	if (!process.env.STATE_testkit_manifest) return;
	const manifest = absolutePath(process.env.STATE_testkit_manifest);
	try {
		lstatSync(manifest);
	} catch (error) {
		if (error instanceof Error && "code" in error && error.code === "ENOENT") {
			return;
		}
		throw error;
	}
	const binary = absolutePath(process.env.STATE_testkit_binary);
	const checksum = process.env.STATE_testkit_checksum;
	if (
		!privateDirectory(dirname(manifest)) ||
		!checksum ||
		!/^[a-f0-9]{64}$/.test(checksum) ||
		identity(binary) !== checksum
	) {
		throw new ActionError("Testkit cleanup state is unavailable or unsafe.");
	}
	const cleanup = spawnSync(binary, ["--cleanup-manifest", manifest], {
		stdio: "ignore",
		timeout: 60_000,
		killSignal: "SIGKILL",
	});
	if (cleanup.status !== 0) {
		throw new ActionError(
			"Testkit fixture cleanup failed; recovery storage was retained. Retry cleanup using the Action outputs.",
		);
	}
}

try {
	if (process.env.STATE_testkit_post === "true") {
		post();
	} else {
		await main();
	}
} catch (error) {
	let message =
		"Testkit Action failed; check runner paths and private storage permissions. Recovery storage was retained.";
	if (error instanceof ActionError || error instanceof SetupError) {
		message = error.message;
	}
	console.error(`::error::${message}`);
	process.exitCode = 1;
}
