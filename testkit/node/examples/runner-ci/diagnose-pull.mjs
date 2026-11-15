// Failure-only CI probe for the public image. It cannot turn acceptance green.
// Keep Docker output private: URLs may contain signed tokens even for public pulls.
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const config = mkdtempSync(join(tmpdir(), "testkit-pull-diagnostic-"));
try {
	const result = spawnSync(
		"docker",
		["pull", "--quiet", "lscr.io/linuxserver/sonarr:latest"],
		{
			env: {
				PATH: process.env.PATH,
				HOME: config,
				DOCKER_CONFIG: config,
				DOCKER_HOST: "unix:///var/run/docker.sock",
			},
			encoding: "utf8",
			timeout: 60_000,
			killSignal: "SIGKILL",
			maxBuffer: 64 * 1024,
		},
	);
	const output = `${result.stderr ?? ""}\n${result.stdout ?? ""}`.toLowerCase();
	let outcome = result.status === 0 ? "passed" : "unclassified-failure";
	if (result.status !== 0) {
		for (const [pattern, category] of [
			[/too many requests|toomanyrequests/, "registry-rate-limit"],
			[/unauthorized|forbidden|denied/, "registry-authorization"],
			[/manifest unknown|not found/, "image-not-found"],
			[/no space left on device/, "host-disk-full"],
			[/x509|certificate|tls handshake/, "tls-failure"],
			[/no such host|temporary failure in name resolution/, "dns-failure"],
			[/timeout|timed out|deadline exceeded/, "network-timeout"],
			[
				/internal server error|bad gateway|service unavailable|gateway timeout/,
				"registry-server-error",
			],
			[
				/connection refused|connection reset|unexpected eof/,
				"network-connection",
			],
		]) {
			if (pattern.test(output)) {
				outcome = category;
				break;
			}
		}
		if (result.error?.code === "ETIMEDOUT") outcome = "probe-timeout";
		if (result.error?.code === "ENOBUFS") outcome = "probe-output-limit";
		if (result.error?.code === "ENOENT") outcome = "docker-unavailable";
	}
	console.log(`Separate public Sonarr pull probe: ${outcome}.`);
	console.log(
		"This probe does not replace or retry the failed acceptance test.",
	);
} finally {
	rmSync(config, { recursive: true, force: true });
}
