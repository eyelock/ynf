# How-to guides

Recipes for a goal you already have. Each gets to the point.

| Guide | Goal |
|---|---|
| [Run ynf locally](run-ynf-locally.md) | Build ynf, point it at a repository with lanes, run it until its work settles, and inspect and replay what it decided |
| [Connect a tracker](connect-a-tracker.md) | Take on JIRA (or any tracker with an MCP server) tickets: declare it, check it, start one |
| [Run the factory image](run-the-factory-image.md) | Run ynf as a job from the factory image, with your harness in it, under the job runner's containment |
| [Measure a lane with shadow mode](measure-a-lane-with-shadow-mode.md) | Run a lane against closed tickets, grade its patches blind against the humans', and read its yield with its confidence interval, before it proposes anything |
| [See ynf in OpenTelemetry](see-ynf-in-opentelemetry.md) | Find where ynf writes its traces, logs and metrics, and follow one step, or one item's history, in them |
| [Cut a release](cut-a-release.md) | Take `develop` to a tagged release on `main`: the release branch, the proof, the tag, and what the release workflow publishes |
| [Test the factory against the sandbox](test-against-the-sandbox.md) | Rebuild the sandbox, calibrate its fixtures, and run the end-to-end acceptance test |
