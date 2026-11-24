#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Usage: sh scripts/setup.sh [--skip-system-packages | --system-only]

Install system tools, supported Go/Node toolchains, and project dependencies.
Run as your normal user; only package installation uses sudo or doas when needed.

  --skip-system-packages  Skip distro packages; provision toolchains without privileges.
  --system-only           Install system tools without project dependencies.
  -h, --help              Show this help.

Supported package managers: apt-get, dnf, yum, apk, pacman, and zypper.
Package versions and availability depend on your enabled distribution repositories.
EOF
}

mode=default
if [ "$#" -gt 1 ]; then
  usage >&2
  exit 1
fi
case "${1-}" in
  '') ;;
  --skip-system-packages) mode=skip ;;
  --system-only) mode=system ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac

project_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
export PATH="$project_root/.tools/go/bin:$project_root/.tools/node/bin:$PATH"
export npm_config_cache="$project_root/.cache/npm"
export GOCACHE="$project_root/.cache/go-build"
export GOPATH="$project_root/.cache/go"
export CGO_ENABLED=0

missing_tools() {
  for tool in bash cc make git ssh curl rg shellcheck tar gzip sha256sum awk mktemp uname; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      printf ' %s' "$tool"
    fi
  done
}

run_privileged() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  elif command -v doas >/dev/null 2>&1; then
    doas "$@"
  else
    printf 'System packages need root, sudo, or doas. Ask an administrator to run:\n' >&2
    printf '  sh scripts/setup.sh --system-only\n' >&2
    printf 'Then rerun setup as your normal user.\n' >&2
    exit 1
  fi
}

install_system_packages() {
  for manager in apt-get dnf yum apk pacman zypper; do
    if command -v "$manager" >/dev/null 2>&1; then
      printf 'Installing system tools with %s.\n' "$manager"
      case "$manager" in
        apt-get)
          run_privileged apt-get update || return
          run_privileged env DEBIAN_FRONTEND=noninteractive apt-get install -y \
            bash build-essential coreutils tar gzip git openssh-client \
            curl ca-certificates tzdata ripgrep shellcheck || return
          ;;
        dnf | yum)
          run_privileged "$manager" install -y \
            bash gcc make libstdc++ coreutils tar gzip git openssh-clients \
            curl ca-certificates tzdata ripgrep ShellCheck || return
          ;;
        apk)
          run_privileged apk add --no-cache \
            bash build-base coreutils tar gzip git openssh-client \
            curl ca-certificates tzdata ripgrep shellcheck || return
          ;;
        pacman)
          # Refresh and upgrade together, including on images without package databases.
          run_privileged pacman -Syu --needed --noconfirm \
            bash gcc make coreutils tar gzip git openssh \
            curl ca-certificates tzdata ripgrep shellcheck || return
          ;;
        zypper)
          run_privileged zypper --non-interactive refresh || return
          run_privileged zypper --non-interactive install \
            bash gcc make libstdc++6 coreutils tar gzip git openssh-clients \
            curl ca-certificates timezone ripgrep ShellCheck || return
          ;;
      esac
      return
    fi
  done

  printf 'No supported package manager found. Install the tools listed in README.md,\n' >&2
  printf 'then run sh scripts/setup.sh --skip-system-packages.\n' >&2
  exit 1
}

missing=$(missing_tools)
if [ "$mode" = system ] || { [ "$mode" = default ] && [ -n "$missing" ]; }; then
  if ! install_system_packages; then
    printf 'System package installation failed. Check repository access and package availability.\n' >&2
    printf 'Some RHEL-family releases need EPEL for ripgrep and ShellCheck.\n' >&2
    exit 1
  fi
fi

missing=$(missing_tools)
if [ -n "$missing" ]; then
  printf 'Missing development tools:%s\n' "$missing" >&2
  printf 'Install them using your distribution packages or supported upstream releases.\n' >&2
  exit 1
fi

if [ "$mode" = system ]; then
  printf 'System tools installed. Run sh scripts/setup.sh as your normal user next.\n'
  exit 0
fi

toolchain_supported() {
  command -v node >/dev/null 2>&1 || return 1
  checked_node_version=$(node --version 2>/dev/null) || return 1
  checked_go_version=unknown
  if [ "$1" = go ] && command -v go >/dev/null 2>&1; then
    checked_go_version=$(GOTOOLCHAIN=local go version 2>/dev/null) || return 1
  fi
  node --input-type=module - "$project_root" "$1" "$checked_go_version" "$checked_node_version" <<'NODE'
import { readFileSync } from 'node:fs';
import process from 'node:process';

function versionParts(version) {
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(version);
  return match ? match.slice(1).map(Number) : null;
}

function atLeast(actual, minimum) {
  for (let index = 0; index < minimum.length; index += 1) {
    if (actual[index] !== minimum[index]) {
      return actual[index] > minimum[index];
    }
  }
  return true;
}

const projectRoot = process.argv[2];
const goMod = readFileSync(`${projectRoot}/go.mod`, 'utf8');
const requiredGo = /^go (\S+)$/m.exec(goMod)?.[1];
const { engines } = JSON.parse(readFileSync(`${projectRoot}/web/package.json`, 'utf8'));
const tool = process.argv[3];
const goVersion = /^go version go(\S+) /.exec(process.argv[4])?.[1] ?? 'unknown';
const nodeVersion = process.argv[5].replace(/^v/, '');

try {
  if (tool === 'go') {
    // Distribution Go builds may append enabled experiments to the release version.
    const go = versionParts(goVersion.replace(/-X:[A-Za-z0-9_,]+$/, ''));
    const minimumGo = versionParts(requiredGo);
    if (!go || !minimumGo || !atLeast(go, minimumGo)) {
      throw new Error(`Go ${requiredGo}+ is required; found ${goVersion}.`);
    }
  } else {
    const node = versionParts(nodeVersion);
    // These are the two range forms used by this project's engines.node setting.
    const supported = engines.node.split(' || ').some((range) => {
      const match = /^(\^|>=)(\d+\.\d+\.\d+)$/.exec(range);
      if (!match) {
        throw new Error(`Unsupported Node.js engine range: ${range}.`);
      }
      const minimum = versionParts(match[2]);
      return node && atLeast(node, minimum) && (match[1] !== '^' || node[0] === minimum[0]);
    });
    if (!supported) {
      throw new Error(`Node.js ${engines.node} is required; found ${nodeVersion}.`);
    }
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
NODE
}

# These releases and archive checksums are pinned to the official release manifests.
# Keep versions aligned with Dockerfile and the minimums in go.mod/package.json.
go_release=1.27.4
node_release=24.22.2

platform() {
  if [ "$(uname -s)" != Linux ]; then
    printf 'Automatic toolchain installation requires Linux.\n' >&2
    return 1
  fi
  case "$(uname -m)" in
    x86_64 | amd64) architecture=amd64 ;;
    aarch64 | arm64) architecture=arm64 ;;
    *)
      printf 'Automatic toolchain installation supports x86_64 and ARM64.\n' >&2
      return 1
      ;;
  esac
  libc=glibc
  if [ -f /etc/alpine-release ] || { command -v ldd >/dev/null 2>&1 && ldd --version 2>&1 | grep -q musl; }; then
    libc=musl
  fi
}

install_archive() (
  tool=$1
  release=$2
  archive=$3
  checksum=$4
  url=$5
  tools_directory="$project_root/.tools"
  destination="$tools_directory/${archive%.tar.gz}"
  lock="$tools_directory/.install-lock"
  stage=
  umask 077
  mkdir -p "$tools_directory"
  if [ -e "$tools_directory/$tool" ] && [ ! -L "$tools_directory/$tool" ]; then
    printf 'Cannot replace unmanaged toolchain directory: %s\n' "$tools_directory/$tool" >&2
    exit 1
  fi
  if ! mkdir "$lock" 2>/dev/null; then
    printf 'Another toolchain installation holds %s.\n' "$lock" >&2
    exit 1
  fi
  cleanup_install() {
    status=$?
    trap - 0
    if [ -n "$stage" ]; then
      rm -rf -- "$stage" || status=1
    fi
    rmdir "$lock" || status=1
    exit "$status"
  }
  trap 'cleanup_install' 0
  trap 'exit 1' HUP INT TERM
  stage=$(mktemp -d "$tools_directory/.install-XXXXXX")
  if [ ! -e "$destination" ]; then
    printf 'Installing %s %s from %s.\n' "$tool" "$release" "$url"
    curl --fail --location --proto '=https' --proto-redir '=https' \
      --retry 3 --connect-timeout 15 --max-time 300 \
      --output "$stage/archive.tar.gz" "$url"
    if ! (cd "$stage" && printf '%s  archive.tar.gz\n' "$checksum" | sha256sum -c -); then
      printf 'SHA-256 verification failed for %s.\n' "$archive" >&2
      exit 1
    fi
    mkdir "$stage/unpacked"
    tar -xzf "$stage/archive.tar.gz" -C "$stage/unpacked" --strip-components=1
    if [ "$tool" = go ]; then
      installed_version=$(GOROOT="$stage/unpacked" GOTOOLCHAIN=local "$stage/unpacked/bin/go" version)
    else
      installed_version=$("$stage/unpacked/bin/node" --version)
    fi
    case "$tool:$installed_version" in
      "go:go version go$release "* | "node:v$release") ;;
      *)
        printf 'Unexpected %s version in verified archive: %s\n' "$tool" "$installed_version" >&2
        exit 1
        ;;
    esac
    mv "$stage/unpacked" "$destination"
  fi
  ln -s "${archive%.tar.gz}" "$stage/current"
  # Both GNU coreutils and BusyBox support -T, which replaces the symlink itself.
  mv -Tf "$stage/current" "$tools_directory/$tool"
  cleanup_install
)

install_node() {
  platform
  case "$architecture:$libc" in
    amd64:glibc)
      node_platform=x64
      checksum=361718c444e49af2ca5a110c83ba6a8901143f23ab285ebbef0341f391f482a9
      ;;
    amd64:musl)
      node_platform=x64-musl
      checksum=11ebd16c8d7819c0675211225505273d7aea305a8dd85a9c0aeb1fbec2343320
      ;;
    arm64:glibc)
      node_platform=arm64
      checksum=95c264150aceb7f3437a29fd6984924f5f8d1511ea3cbd6c508b83f6c555dae2
      ;;
    arm64:musl)
      if [ "$mode" = skip ]; then
        printf 'ARM64 Alpine needs supported nodejs/npm packages; rerun without --skip-system-packages.\n' >&2
        return 1
      fi
      run_privileged apk add --no-cache nodejs npm
      return
      ;;
  esac
  archive="node-v$node_release-linux-$node_platform.tar.gz"
  install_archive node "$node_release" "$archive" "$checksum" "https://nodejs.org/dist/v$node_release/$archive"
}

install_go() {
  platform
  case "$architecture" in
    amd64) checksum=7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840 ;;
    arm64) checksum=3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92 ;;
  esac
  archive="go$go_release.linux-$architecture.tar.gz"
  install_archive go "$go_release" "$archive" "$checksum" "https://go.dev/dl/$archive"
}

if ! toolchain_supported node >/dev/null 2>&1 || ! command -v npm >/dev/null 2>&1; then
  install_node
  hash -r
fi
toolchain_supported node
if ! toolchain_supported go >/dev/null 2>&1; then
  install_go
  hash -r
fi
toolchain_supported go
GOTOOLCHAIN=local go version
node --version
npm --version
npm --prefix "$project_root/web" ci --engine-strict
npm --prefix "$project_root/web" run check:tooling
(cd "$project_root" && go mod download)
printf 'Setup complete. Run make build.\n'
