import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { docker } from "./docker.mjs";

const witness = JSON.parse(readFileSync("witness.json", "utf8"));
assert.match(witness.project, /^testkit-[a-f0-9]{24}$/);
const filter = `label=com.docker.compose.project=${witness.project}`;
// Run before the Action post hook: recovery must not conceal failed normal cleanup.
assert.equal(docker("ps", "--all", "--quiet", "--filter", filter), "");
assert.equal(docker("network", "ls", "--quiet", "--filter", filter), "");
assert.equal(docker("volume", "ls", "--quiet", "--filter", filter), "");
assert.equal(process.env.SONARR_URL, undefined);
assert.equal(process.env.SONARR_API_KEY, undefined);
const report = JSON.parse(
	readFileSync("self-host-testkit-report/report.json", "utf8"),
);
assert.equal(report.schemaVersion, 1);
assert.deepEqual(report.tests, { status: "passed", exitCode: 0 });
assert.equal(report.cleanup, "passed");
assert.equal(report.interrupted, false);
assert.equal(report.exitCode, 0);
assert.equal(report.apps.length, 1);
const [app] = report.apps;
assert.equal(app.name, "sonarr");
assert.equal(app.app, "sonarr");
assert.equal(app.status, "ready");
assert.equal(app.imageId, witness.imageId);
assert.ok(app.durationMs > 0 && app.durationMs <= report.durationMs);
assert.match(app.image, /sonarr/);
assert.ok(app.repoDigests.length > 0);
for (const digest of app.repoDigests)
	assert.match(digest, /@sha256:[a-f0-9]{64}$/);
assert.deepEqual(Object.keys(app).sort(), [
	"app",
	"durationMs",
	"image",
	"imageId",
	"name",
	"repoDigests",
	"status",
]);
assert.deepEqual(Object.keys(report).sort(), [
	"apps",
	"cleanup",
	"durationMs",
	"exitCode",
	"interrupted",
	"schemaVersion",
	"startedAt",
	"tests",
]);
const markdown = readFileSync("self-host-testkit-report/report.md", "utf8");
assert.ok(markdown.includes("Test command: **passed**. Cleanup: **passed**."));
assert.ok(markdown.includes("| sonarr | sonarr | ready |"));
console.log(
	"Source runner: API assertions, image provenance, report and scoped cleanup passed.",
);
