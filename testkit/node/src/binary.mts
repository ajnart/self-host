import { existsSync, lstatSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { isAbsolute, join } from "node:path";

const metadata: unknown = JSON.parse(
	readFileSync(new URL("./package.json", import.meta.url), "utf8"),
);
if (
	typeof metadata !== "object" ||
	metadata === null ||
	!("version" in metadata) ||
	typeof metadata.version !== "string" ||
	!/^\d+\.\d+\.\d+$/.test(metadata.version)
) {
	throw new Error("Invalid testkit package version.");
}

export const version = metadata.version;

export function binaryDirectory(): string {
	let cache = join(homedir(), ".cache");
	if (process.env.XDG_CACHE_HOME && isAbsolute(process.env.XDG_CACHE_HOME)) {
		cache = process.env.XDG_CACHE_HOME;
	}
	return join(
		cache,
		"self-host-testkit",
		version,
		`${process.platform}-${process.arch}`,
	);
}

export function privateDirectory(path: string): boolean {
	const info = lstatSync(path);
	return (
		info.isDirectory() &&
		info.uid === process.getuid?.() &&
		(info.mode & 0o077) === 0
	);
}

export function installedBinary(): string {
	const directory = binaryDirectory();
	const binary = join(directory, "self-host-testkit");
	if (!existsSync(binary)) return "self-host-testkit";
	const info = lstatSync(binary);
	if (
		!privateDirectory(directory) ||
		!info.isFile() ||
		info.uid !== process.getuid?.() ||
		(info.mode & 0o077) !== 0 ||
		(info.mode & 0o100) === 0
	) {
		throw new Error(
			"Testkit helper cache is not private; set SELF_HOST_TESTKIT_BINARY explicitly.",
		);
	}
	return binary;
}
