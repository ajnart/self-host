import { execFileSync } from "node:child_process";

// Inspect only public identity/cap fields. Never dump container environment.
export function docker(...args) {
	try {
		return execFileSync("docker", args, {
			encoding: "utf8",
			stdio: ["ignore", "pipe", "ignore"],
			timeout: 10_000,
		}).trim();
	} catch {
		throw new Error("Docker acceptance inspection failed.");
	}
}
