# cli-test-manager

`tester` is a small command-line program that runs a project's tests, keeps their history, and shows it back, in plain commands or in an interactive menu. It knows nothing about any particular project: the project describes its tests in a file it owns, and keeps the history in a file it owns.

- It reads results in two standard formats only, **TAP** and **JUnit XML**. A project whose tests print something else converts it with a small wrapper. Besides pass or fail, it keeps the numbers a test reports with a result (see "Measurements").
- It stores each run in a **SQLite file inside the project** (pure Go, no server).
- Secrets are passed to the tests from your environment by **name**. The values are scrubbed from everything stored or shown.
- It starts every command in a few milliseconds, so it is safe to use from a git hook.

It can also **pull** results a CI service produced (a GitHub Actions workflow that uploads a JUnit file), so local and remote runs sit in one history. Pulling is new and has not yet been tried against a real repository.

## Build

```
go build -o tester ./cmd/tester
```

Go 1.27 (the version in `go.mod`). The binary is self-contained. The menu library, Bubble Tea, is used through a patched copy in `third_party/bubbletea` (see `PATCHED.md` there); nothing else needs to be installed.

## The manifest

A project puts a `test-manifest.yaml` at its root. `examples/test-manifest.yaml` is a commented example. In short:

| Section | What it holds |
|---|---|
| `project`, `manifest_version`, `history` | the project's name, the schema version (2), and where the history file lives, relative to the manifest |
| `features` | a map from each feature's name to its settings. `paths` (optional) lists glob patterns of the files that belong to the feature (`**` matches any number of folders). `general` always exists, for tests and files that belong to none |
| `environments` | where tests can run (`local`, `staging`...), each with `vars` (plain values), `secrets` (**names** only, never values) and `actions`: what the tester may do there (`run`, `pull`, `import`; omitted means `run` and `pull`) |
| `runners` | how to start a kind of test: the command as an argument list with `{files}` (and `{report}` for a result file), the result `format` (`tap` or `junit`), `output` (`stdout` or `file`), the environments it serves (each must allow `run`), a `timeout`, and an optional `version` command whose first line is recorded with every run |
| `pull` | optional: where results produced elsewhere come from. The GitHub `repo` (`owner/name`), the `workflow`, the `artifact` name, the `environment` those results belong to (it must allow `pull`), `lookback_days` for the first pull, and `token`: the **name** of the environment variable holding a GitHub token that can read the repository's Actions (never listed as an environment secret, so tests never receive it) |
| `tests` | the tests, in order. The order is the numbering and is append-only, so history stays readable. Each has an `id` that never changes, a `name`, a `feature`, a `runner`, a `file` and an optional `tag` |

| `preconditions` | optional, inside an environment, a runner or a test: what the person must have done before those tests can run. Each has a `name`, a `check` (a command as an argument list, no shell; exit 0 means ready) and a `message` telling the person what to do. See "Preconditions" below |

The tester looks for the manifest in the current folder and the folders above it, unless `--manifest PATH` names it (a file or a folder).

`tester check` validates the manifest and that every test file exists.

## Preconditions

Some things only the person can do first: bring the database up to date, start a service. The tester never does them, because it knows nothing of the project's tools. It checks, and when something is missing it **starts nothing**, says what to do, and records that the run was refused.

```yaml
environments:
  local:
    preconditions:
      - name: migrations-applied
        check: ["bash", "scripts/check-migrations-applied.sh"]
        message: "Run `supabase db reset`, then run again."
```

- **Where.** On an environment (checked before any test of it), on a runner (before any test of it) or on a test. Only the tests that are about to run count: a precondition of a test that was left out by `--test`, `--feature`, `--changed` or `--unplayed` is not checked. The same name in several places must mean one definition, and its check runs once.
- **The check** runs in the project folder with the environment the tests get, and stops after 30 seconds (which counts as not met). It must only look, never change anything: it can run more than once, because `run` checks before it asks "Are you sure?" and again before it starts. What it printed first (or its exit code) is shown as "the check said".
- **When one is not met**, nothing starts. Every precondition that is not met is listed together, with its message. The run is saved as a run with no results, shown as `NOT RUN` in `tester history`, with the commit, the trigger and the reasons. The command exits with **2**, so a push hook stops the push. No question is asked about a run that cannot start.
- **A refusal is not a pass or a fail.** It counts as no test being played: `--unplayed` and `counts` ignore it.
- **`--dry-run`** reports the same refusal and exit code, and saves nothing.
- **Ctrl-C** during a check saves nothing, as everywhere. A check that cannot be started at all (the program is missing) is a mistake in the manifest, reported as an error, not as a refusal.

## Measurements

A load or network tool reports numbers (a response time, a rate, a share of failures), not just pass or fail. The tester keeps them with the result they belong to, for passing results too, so a number can be read back and compared across runs. A wrapper that turns a tool's output into TAP or JUnit puts them in the file:

**TAP:** a YAML block, indented, directly after the test line.

```
ok 1 - sign-in p95
  ---
  duration_ms: 187
  p95_ms: 187
  rate_per_s: 101.5
  ...
```

**JUnit:** `<property>` elements inside the test case's `<properties>`.

```xml
<testcase classname="k6" name="sign-in p95" time="0.187">
  <properties><property name="p95_ms" value="187"/></properties>
</testcase>
```

- A measurement is a **name and a number**. The tester does not interpret either; put the unit in the name (`p95_ms`). Values that are not numbers (text, dates, `NaN`) and nested YAML keys are ignored.
- In TAP, `duration_ms` is the result's duration (as Node's test runner prints it), not a measurement. In JUnit the duration is the `time` attribute, as before.
- Properties of a JUnit `<testsuite>` are ignored: they describe the run, not a result.
- At most 64 measurements are kept per result and a name is cut at 128 bytes, so one noisy file cannot swell the history. Within a result, a repeated name takes the last value.
- They are shown by `tester history <n> --detail` (and the menu's `d` key). The history file stores them in the `measurement` table (schema 6, added by an automatic upgrade; runs saved before it simply have none).

## Commands

| Command | What it does |
|---|---|
| `tester run [--env NAME] [--feature NAME] [--test ID ...] [--changed PATH ... \| -] [--unplayed] [--dry-run] [--trigger manual\|hook] [--yes]` | runs tests and saves the run. `--dry-run` decides what would run and prints one line `would run  <id>` per test (and one `skipped  <id>  (reason)` per test left out), then stops: nothing is started, saved or asked, no history file is created, and it exits 0 whether or not anything would run (2 if the request could not run), so a script can look for those lines. A run of every test asks "Are you sure?" at a terminal; `--yes` skips it, and a hook (no terminal) is never asked, nor is a narrowed run. `--changed` keeps the features the changed files select (their `paths` patterns, the feature of a test whose own file it is, every feature for the manifest, `general` for a file in no feature; `-` reads one path per line from standard input, for example from `git diff --name-only`). `--unplayed` leaves out each test that already passed in this environment with its file as it is now, and lists them with the run that passed them. When the narrowing leaves nothing to run, that is a success: exit 0, nothing saved |
| `tester history [--env] [--feature] [--source] [--limit] [--offset]` | lists runs, newest first |
| `tester history <n> [--checks] [--detail]` | shows one run: one row per test with its verdict (a test that printed several checks shows how many and lists only its failed ones; `--checks` lists every check), the diagnostic when a command crashed or timed out, and with `--detail` what the test tool itself reported: its measurements, then its stack traces. A run that has either says so under the results |
| `tester pull` | brings in the results of the workflow's runs that are not in the history yet, stopping at the first run it already has. A run still going is kept as a placeholder and checked again next time; a run whose file expired or is missing is recorded as such. Exit 0 whatever the pulled tests did, 2 if it could not pull |
| `tester import [--env NAME] [--test ID] [--format junit\|tap] [--commit SHA] [--tool name=version] [--when TIME] <file \| ->` | records a result file that came from outside (the output of a load or network tool the tester never starts) as one run. The environment must allow `import`. Results go to the test their suite or class name points at, or all to `--test`. The same file into the same environment twice is stored once. Exit 0 whatever the results were, 2 if it could not import |
| `tester tests [--feature NAME]` | lists the tests with their numbers |
| `tester tests show [--run N] <id>` | shows a test's file **as a run recorded it**, never the file on disk (that is a live file, and it may have changed). Without `--run` it is the latest run that played the test; a test no run has played has no snapshot yet |
| `tester history <n> --files` / `--file <path>` | lists the files a run was made with (the manifest, the tests' files, and the project files matching the path patterns of the features that ran), or prints one as it was in that run. Only runs the tester started have them |
| `tester counts [--by test\|feature] [--env] [--feature] [--since DATE]` | shows how each test has done, counting a test once per run (its verdict is the worst of its checks), or with `--by feature` how often each feature's tests were played: RUNS, PLAYS (one per test per run) and FAILED |
| `tester check` | checks the manifest |
| `tester projects list \| add \| rename \| remove` | manages the list of projects the program knows (below) |
| `tester menu` | opens the interactive menu (needs a terminal) |

Exit codes: 0 all passed, 1 a test failed, 2 could not run (a wrong request, or a precondition that is not met), 130 cancelled.

A run saves **nothing** when it is cancelled (Ctrl-C), so history only ever holds complete runs. The one other kind of row is a run that was **refused** because a precondition was not met: it shows as `NOT RUN`, has no results, and says why.

## The list of projects

The program keeps one small file, `projects.yaml`, in your config folder (`tester/` inside the folder Go reports as the user config folder: `$XDG_CONFIG_HOME` or `~/.config` on Linux). It holds the folder of each project, its name and an optional nickname, and nothing else: each project's manifest and history stay in the project.

- A project is added when you use `run` or `history` in it. At a terminal you are asked for an optional nickname.
- `tester projects add [--name nickname] [folder]` adds one yourself; `rename` and `remove` change or forget it. Forgetting never touches the project's folder.
- A project whose folder has moved is shown as "folder not found" and can be forgotten.

## The menu

`tester menu` walks: **project → feature → environment → action** (the environment step is skipped when the manifest has one). Every screen shows the path so far, `Esc` goes back one screen, `q` or Ctrl-C leaves.

| Screen | What it does |
|---|---|
| Projects | the known projects. `Enter` opens one, `r` sets a nickname, `x` forgets it |
| Features, environments | each with its number of tests, and how many of them can run in that environment |
| Run history | the runs of that feature in that environment. `Enter` opens one: its results, its diagnostic, and `c` shows every check of a test that printed several, `d` shows or hides the test tool's detail |
| Counts | each test's runs, passes and failures, and its last result |
| Tests | the tests of the feature. `Enter` shows a test's code with line numbers |
| Run all | asks first, with the real count (`Run 8 of 9 tests ... (1 skipped)`), then runs. When a precondition is not met it does not ask: the run is refused and recorded, and the saved run's screen shows `NOT RUN` with what to do (if even the history cannot be written, the screen says what to do and that it could not be recorded) |
| Run several | tick tests with space (`a` for all), `Enter` runs the ticked ones |
| Running | lists tests as they finish. `Esc` or Ctrl-C stops it and saves nothing |

When the open project's manifest has a `pull` section, the menu also pulls (see `tester pull`): when you open the project, and again with `p`. A line under the screen says how the link is doing (`GitHub: connected · pulled 14:05 (2 new runs)`, or why it is not connected). Every two minutes it asks GitHub, with one list call and no downloads, whether there are runs it has not pulled, and shows a `●` beside `p pull` when there are. The Run history and Counts screens read again when a pull brings runs in.

## How the code is organised

Dependencies point inward: the screens and commands call services, the services call small interfaces, and the interfaces are implemented at the edge.

| Package | Role |
|---|---|
| `core` | the shared types and the interfaces (store, runner, credentials) |
| `manifest`, `manifestfile` | validating a manifest, and finding and loading the file |
| `parse/tap`, `parse/junit` | reading results |
| `runner` | starting a command as a child process, with a timeout and bounded output |
| `redact`, `creds/env` | scrubbing secrets, and reading them from the environment |
| `store/sqlite` | the history file |
| `execute` | preview and run: the logic of a run |
| `pull`, `source/github` | pulling: the service (what is new, matching results to tests, storing once per run and attempt) and the GitHub adapter behind the `ResultSource` interface |
| `query` | history, one run, counts, the test list, a test's code |
| `view` | the text of a run, shared by the command and the menu |
| `projects` | the list of projects |
| `cli`, `tui`, `cmd/tester` | the plain commands, the menu, and the program |

## Tests

```
go test ./...
```

The tests need no network and no project: they use fake commands, temporary folders and synthetic results.
