# msggw — macOS

There is no macOS package here — no `.pkg`, no Homebrew formula, nothing a
package manager tracks. This directory just builds a plain
`message-gateway` binary, the same way you'd build it on any other machine
with Go installed. The two scripts exist only because a Mac won't already
have the right Go toolchain lying around.

## 1. Get the right Go toolchain

```sh
./goget-macos.sh
```

With no arguments, this reads the Go version the rest of the project
builds with from [`../go.version`](../go.version) (currently `1.27.0`),
detects whether you're on Apple Silicon or Intel (`arm64`/`amd64`), and
installs it under `/opt/go-versions/<version>`, symlinked from `/opt/go` —
the same layout the other `__*/` builders use, so if you already have Go
set up for one of those, this fits right in.

Add it to your `PATH` (once, e.g. in `~/.zshrc` or `~/.bash_profile`):

```sh
export PATH="/opt/go/bin:$PATH"
```

You can pin an explicit version and/or architecture instead:

```sh
./goget-macos.sh 1.27.0        # explicit version, this Mac's own arch
./goget-macos.sh 1.27.0 amd64  # explicit version, force an Intel build
```

Since this writes under `/opt`, it needs `sudo` — it'll prompt for your
password.

## 2. Build the binary

```sh
./build-macos.sh
```

This builds `message-gateway` with `CGO_ENABLED=0` (no C toolchain needed)
and drops it at `/opt/sbin/message-gateway` by default — the same install
path every other platform this project packages for uses. It works no
matter which directory you run it from; it finds the Go module (`../src`)
relative to its own location, not your current directory.

If you're not on `main`/`develop`, the binary is named
`message-gateway-<branch>` instead, so a build off a feature branch never
overwrites your main build.

Useful flags:

```sh
./build-macos.sh ~/bin                    # build to a different directory
./build-macos.sh -b msg-gw                # build under a different binary name
./build-macos.sh --dry-run                # build, confirm it worked, then delete it
```

## 3. Confirm it

```sh
/opt/sbin/message-gateway version
```

From here on, follow [`../docs/RUNNING.md`](../docs/RUNNING.md) to configure
and run it — nothing else about setup is macOS-specific.
