# cli-test-manager

`tester` is a small command-line program that runs a project's tests, keeps their history, and shows it back, in plain commands or in an interactive menu. It knows nothing about any particular project: the project describes its tests in a file it owns, and keeps the history in a file it owns.

- It reads results in two standard formats only, **TAP** and **JUnit XML**. A project whose tests print something else converts it with a small wrapper.
- It stores each run in a **SQLite file inside the project** (pure Go, no server).
- Secrets are passed to the tests from your environment by **name**. The values are scrubbed from everything stored or shown.
- It starts every command in a few milliseconds, so it is safe to use from a git hook.

Not built yet: pulling results produced elsewhere (for example a GitHub workflow). The manifest has a `pull:` section reserved for it.

## Build

```
go build -o tester ./cmd/tester
```

Go 1.27 (the version in `go.mod`). The binary is self-contained. The menu library, Bubble Tea, is used through a patched copy in `third_party/bubbletea` (see `PATCHED.md` there); nothing else needs to be installed.

## The manifest

A project puts a `test-manifest.yaml` at its root. `examples/test-manifest.yaml` is a commented example. In short:

| Section | What it holds |
|---|---|
| `project`, `manifest_version`, `history` | the project's name, the schema version (1), and where the history file lives, relative to the manifest |
| `features` | the names tests are grouped under. `general` always exists, for tests that belong to none |
| `environments` | where tests can run (`local`, `staging`...), each with `vars` (plain values) and `secrets` (**names** only, never values) |
| `runners` | how to start a kind of test: the command as an argument list with `{files}` (and `{report}` for a result file), the result `format` (`tap` or `junit`), `output` (`stdout` or `file`), the environments it serves, and a `timeout` |
| `tests` | the tests, in order. The order is the numbering and is append-only, so history stays readable. Each has an `id` that never changes, a `name`, a `feature`, a `runner`, a `file` and an optional `tag` |

The tester looks for the manifest in the current folder and the folders above it, unless `--manifest PATH` names it (a file or a folder).

`tester check` validates the manifest and that every test file exists.

## Commands

| Command | What it does |
|---|---|
| `tester run [--env NAME] [--feature NAME] [--test ID ...] [--yes]` | runs tests and saves the run. A run of every test asks "Are you sure?" at a terminal; `--yes` skips it, and a hook (no terminal) is never asked |
| `tester history [--env] [--feature] [--source] [--limit] [--offset]` | lists runs, newest first |
| `tester history <n> [--detail]` | shows one run: its results, the diagnostic when a command crashed or timed out, and with `--detail` the test tool's own stack traces |
| `tester tests [--feature NAME]` | lists the tests with their numbers |
| `tester tests show <id>` | shows a test's file |
| `tester counts [--env] [--feature] [--since DATE]` | shows how each test has done |
| `tester check` | checks the manifest |
| `tester projects list \| add \| rename \| remove` | manages the list of projects the program knows (below) |
| `tester menu` | opens the interactive menu (needs a terminal) |

Exit codes: 0 all passed, 1 a test failed, 2 could not run or a wrong request, 130 cancelled.

A run saves **nothing** when it is cancelled (Ctrl-C), so history only ever holds complete runs.

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
| Run history | the runs of that feature in that environment. `Enter` opens one: its results, its diagnostic, and `d` shows or hides the test tool's detail |
| Counts | each test's runs, passes and failures, and its last result |
| Tests | the tests of the feature. `Enter` shows a test's code with line numbers |
| Run all | asks first, with the real count (`Run 8 of 9 tests ... (1 skipped)`), then runs |
| Run several | tick tests with space (`a` for all), `Enter` runs the ticked ones |
| Running | lists tests as they finish. `Esc` or Ctrl-C stops it and saves nothing |

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
| `query` | history, one run, counts, the test list, a test's code |
| `view` | the text of a run, shared by the command and the menu |
| `projects` | the list of projects |
| `cli`, `tui`, `cmd/tester` | the plain commands, the menu, and the program |

## Tests

```
go test ./...
```

The tests need no network and no project: they use fake commands, temporary folders and synthetic results.
