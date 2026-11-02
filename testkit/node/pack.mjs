import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { appendFileSync, readFileSync, renameSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const { version } = JSON.parse(
	readFileSync(join(root, "package.json"), "utf8"),
);
if (version !== process.env.SELF_HOST_RELEASE_VERSION) {
	throw new Error(
		"Testkit package version must match SELF_HOST_RELEASE_VERSION.",
	);
}
const destination = resolve(root, "../../dist");
const packed = spawnSync(
	"npm",
	["pack", "--ignore-scripts", "--json", "--pack-destination", destination],
	{
		cwd: root,
		stdio: ["ignore", "pipe", "ignore"],
		timeout: 60000,
		maxBuffer: 1024 * 1024,
	},
);
if (packed.status !== 0) throw new Error("Testkit package creation failed.");
const [artifact] = JSON.parse(packed.stdout.toString());
const filename = `self-host-testkit-${version}.tgz`;
const expected = [
	"README.md",
	"LICENSE",
	"binary.mjs",
	"examples/sonarr.mjs",
	"index.d.mts",
	"index.mjs",
	"package.json",
	"reasons.d.mts",
	"reasons.mjs",
	"setup.mjs",
].sort();
if (
	artifact.filename !== `ajnart-self-host-testkit-${version}.tgz` ||
	JSON.stringify(artifact.files.map((file) => file.path).sort()) !==
		JSON.stringify(expected)
) {
	throw new Error("Unexpected testkit package contents.");
}
renameSync(join(destination, artifact.filename), join(destination, filename));
const digest = createHash("sha256")
	.update(readFileSync(join(destination, filename)))
	.digest("hex");
appendFileSync(join(destination, "checksums.txt"), `${digest}  ${filename}\n`);
