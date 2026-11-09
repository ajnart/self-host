#!/usr/bin/env node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { RunConfigError, parseRunConfig, runTests, writeRunReport, } from "./run.mjs";
const usage = `Usage: self-host-testkit-run --apps sonarr,radarr [--concurrency 2] [--timeout-ms 720000] [--report-dir self-host-testkit-report] -- npm test
       self-host-testkit-run --config self-host.testkit.json -- npm test

Runs your existing tests with SELF_HOST_<APP>_URL, _API_KEY, _USERNAME, _PASSWORD and _EMAIL.
Use a JSON config to map these to environment names your tests already consume.
Apps start in parallel by default; all remain running until tests finish. Budget resources for every declared App.
Reports contain fixture setup and command outcomes, never credentials or raw test output.`;
try {
    const args = process.argv.slice(2);
    if (!args.length || args[0] === "--help" || args[0] === "-h") {
        console.log(usage);
    }
    else {
        const divider = args.indexOf("--");
        if (divider < 0 || divider === args.length - 1)
            throw new RunConfigError();
        const command = args.slice(divider + 1);
        const flags = args.slice(0, divider);
        const values = new Map();
        for (let i = 0; i < flags.length; i += 2) {
            const flag = flags[i];
            const value = flags[i + 1];
            if (!flag ||
                ![
                    "--apps",
                    "--config",
                    "--concurrency",
                    "--timeout-ms",
                    "--report-dir",
                ].includes(flag) ||
                !value ||
                values.has(flag))
                throw new RunConfigError();
            values.set(flag, value);
        }
        if (values.has("--apps") === values.has("--config"))
            throw new RunConfigError();
        let input;
        if (values.has("--config"))
            input = JSON.parse(readFileSync(values.get("--config"), "utf8"));
        else {
            const apps = values.get("--apps")
                .split(",")
                .map((app) => app.trim());
            if (new Set(apps).size !== apps.length)
                throw new RunConfigError();
            input = { apps: Object.fromEntries(apps.map((app) => [app, app])) };
        }
        const config = parseRunConfig(input);
        if (values.has("--concurrency"))
            config.concurrency = Number(values.get("--concurrency"));
        if (values.has("--timeout-ms"))
            config.timeoutMs = Number(values.get("--timeout-ms"));
        const controller = new AbortController();
        const abort = () => controller.abort();
        process.on("SIGINT", abort);
        process.on("SIGTERM", abort);
        try {
            const report = await runTests(config, command, {
                signal: controller.signal,
            });
            writeRunReport(report, resolve(values.get("--report-dir") ?? "self-host-testkit-report"));
            console.log(`self-host testkit: tests ${report.tests.status}; cleanup ${report.cleanup}.`);
            process.exitCode = report.exitCode;
        }
        finally {
            process.removeListener("SIGINT", abort);
            process.removeListener("SIGTERM", abort);
        }
    }
}
catch (error) {
    console.error(error instanceof RunConfigError
        ? error.message
        : "Testkit run failed. Check config, command, and report directory permissions.");
    process.exitCode = 1;
}
