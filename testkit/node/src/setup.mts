#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
	chmodSync,
	lstatSync,
	mkdirSync,
	mkdtempSync,
	renameSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { join } from "node:path";
import {
	binaryDirectory,
	installedBinary,
	privateDirectory,
	version,
} from "./binary.mjs";

async function download(url: string, limit: number): Promise<Buffer> {
	const response = await fetch(url, { signal: AbortSignal.timeout(60_000) });
	if (!response.ok || !response.body)
		throw new Error(
			"Release download failed; check connectivity and that the matching release exists.",
		);
	const chunks: Uint8Array[] = [];
	let size = 0;
	for await (const chunk of response.body) {
		size += chunk.length;
		if (size > limit) {
			throw new Error("Release download exceeds the size limit.");
		}
		chunks.push(chunk);
	}
	return Buffer.concat(chunks);
}

async function setup(): Promise<void> {
	if (process.argv.slice(2).length !== 0) {
		if (process.argv.length === 3 && process.argv[2] === "--help") {
			console.log(
				"self-host-testkit-setup: download and verify the matching helper for Linux/macOS x64/arm64. Requires tar. Does not start Docker or modify PATH.",
			);
			return;
		}
		throw new Error("Usage: self-host-testkit-setup [--help]");
	}
	if (
		!(process.platform === "linux" || process.platform === "darwin") ||
		!(process.arch === "x64" || process.arch === "arm64")
	) {
		throw new Error(
			"Testkit supports Linux and macOS on x64 or arm64. On Windows, use WSL2 with a local Unix Docker socket.",
		);
	}
	const directory = binaryDirectory();
	mkdirSync(directory, { recursive: true, mode: 0o700 });
	if (!privateDirectory(directory))
		throw new Error(
			"Testkit helper cache must be private and owned by the current user.",
		);
	if (installedBinary() !== "self-host-testkit") {
		console.log(`Testkit helper ${version} is already installed.`);
		return;
	}
	let arch: string = process.arch;
	if (arch === "x64") arch = "amd64";
	const archive = `self-host_${version}_${process.platform}_${arch}.tar.gz`;
	const base = `https://github.com/ajnart/self-host/releases/download/v${version}`;
	console.log(
		`Downloading and verifying testkit helper ${version} for ${process.platform}/${arch}…`,
	);
	const checksums = (
		await download(`${base}/checksums.txt`, 1024 * 1024)
	).toString("utf8");
	const matches = checksums
		.split("\n")
		.map((line) => line.trim().split(/\s+/))
		.filter((entry) => entry[1] === archive);
	if (matches.length !== 1 || !/^[a-f0-9]{64}$/.test(matches[0]?.[0] ?? ""))
		throw new Error("Release lacks an unambiguous helper checksum.");
	const data = await download(`${base}/${archive}`, 128 * 1024 * 1024);
	if (createHash("sha256").update(data).digest("hex") !== matches[0]?.[0])
		throw new Error("Helper checksum mismatch; nothing installed.");
	const staging = mkdtempSync(join(directory, ".download-"));
	try {
		const path = join(staging, archive);
		writeFileSync(path, data, { mode: 0o600 });
		const result = spawnSync(
			"tar",
			["-xzf", path, "-C", staging, "self-host-testkit"],
			{ stdio: "ignore", timeout: 30_000 },
		);
		if (result.status !== 0)
			throw new Error("Helper extraction failed; make sure tar is installed.");
		const binary = join(staging, "self-host-testkit");
		if (!lstatSync(binary).isFile())
			throw new Error("Release did not contain a regular helper binary.");
		chmodSync(binary, 0o700);
		renameSync(binary, join(directory, "self-host-testkit"));
	} finally {
		rmSync(staging, { recursive: true, force: true });
	}
	console.log(
		"Helper ready. createSelfHost() will find it automatically; no PATH or environment changes needed.",
	);
}

try {
	await setup();
} catch (error) {
	let message = "Unknown setup failure.";
	if (error instanceof Error) message = error.message;
	console.error("Testkit setup failed.", message);
	process.exitCode = 1;
}
