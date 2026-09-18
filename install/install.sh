#!/usr/bin/env bash
#
# Spinneret — guided Docker install.
#
#   curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh -o install.sh
#   less install.sh          # read it first; you are about to run it
#   bash install.sh
#
# Docker only. Installing without Docker means putting PostgreSQL, Valkey,
# ClickHouse, Go and Node on the host yourself and wiring them together, which
# is a document rather than a script: documents/en/02-installation.md.
#
# What this script will and will not do
# -------------------------------------
# It writes only inside the directory you choose, plus Docker's own named
# volumes. If the parent directories of that path do not exist yet it creates
# them, with sudo, after printing the command; nothing else outside the
# directory is touched. It never edits a file out there, never adds a cron job,
# never opens a firewall port, and never installs anything without asking first.
#
# It uses sudo only for: installing Docker (if you say yes), starting the Docker
# service, and creating the install directory when that directory needs root.
# Each of those asks first and prints the exact command.
#
# One thing about this deployment is different from most: the key-encryption key
# in deploy/compose/secrets/kek.key is what every stored credential, proxy URL
# and vault secret is encrypted with. There is no recovery path without it. This
# script generates it once, never overwrites it, and tells you to back it up
# before it tells you anything else.
#
# The Chinese version of this script is install.zh.sh in the same directory.
# The two are kept structurally identical; only the messages differ.

set -euo pipefail
IFS=$'\n\t'

# ---------------------------------------------------------------- constants --

readonly REPO_URL="https://github.com/TikHub/Spinneret.git"
readonly REPO_RAW="https://raw.githubusercontent.com/TikHub/Spinneret/main"
readonly REPO_API="https://api.github.com/repos/TikHub/Spinneret"
readonly DOCKER_INSTALL_URL="https://get.docker.com"
readonly DOCS_INSTALL="https://github.com/TikHub/Spinneret/blob/main/documents/en/02-installation.md"
readonly DOCS_EN="https://github.com/TikHub/Spinneret/blob/main/documents/en/01-quickstart.md"
readonly DOCS_ZH="https://github.com/TikHub/Spinneret/blob/main/documents/zh/01-quickstart.md"

# Compose 2.24 is the first release with the `!reset` and `!override` merge tags,
# and this script writes override files that use both. Anything older fails in
# ways that read like a problem with this project.
readonly COMPOSE_MIN="2.24"

# The published multi-arch image. SPINNERET_IMAGE replaces the repository part
# so a private mirror or a fork can be used without editing this script.
readonly DEFAULT_IMAGE="ghcr.io/tikhub/spinneret"

# How long to wait for /readyz to report every dependency ok. A cold host has to
# pull five images, initialise PostgreSQL, let ClickHouse create its schema and
# let the first replica build its hot state before this can pass.
readonly READY_TIMEOUT=300

# Overridable for testing, so a trial run cannot touch a real `spinneret` stack.
# Validated because it is written into the generated spnrctl: anything that is
# not a plain name would end up as shell syntax there. It is also passed as
# `docker compose -p`, which overrides the `name: spinneret` in the compose file.
PROJECT="${SPINNERET_PROJECT:-spinneret}"
case "$PROJECT" in
  ''|*[!a-zA-Z0-9_-]*)
    printf 'error: SPINNERET_PROJECT must be letters, digits, dash or underscore.\n' >&2
    exit 1 ;;
esac

# ------------------------------------------------------------------ output --

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BLUE=$'\033[36m'
else
  C_RESET=''; C_BOLD=''; C_DIM=''; C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''
fi

step() { printf '\n%s==>%s %s%s%s\n' "$C_BLUE" "$C_RESET" "$C_BOLD" "$*" "$C_RESET"; }
info() { printf '    %s\n' "$*"; }
dim()  { printf '    %s%s%s\n' "$C_DIM" "$*" "$C_RESET"; }
ok()   { printf '    %s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '    %s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
die()  { printf '\n%serror:%s %s\n\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }

# ------------------------------------------------------------------- input --
#
# Reading input when the script is piped is the thing that breaks naive
# installers: with `curl … | bash`, stdin IS the script, so `read` swallows the
# script's own remaining lines. So input always comes from the terminal
# explicitly, and if there is no terminal the script says so instead of
# silently taking defaults for questions like "which address do I publish on".

TTY_IN=""
if [ -t 0 ]; then
  TTY_IN="/dev/stdin"
elif [ -r /dev/tty ] && { : >/dev/tty; } 2>/dev/null; then
  # /dev/tty can be readable and still not usable, so probe writability too.
  TTY_IN="/dev/tty"
fi

ASSUME_YES=0
CHECK_ONLY=0
MANAGE_ONLY=0

# Anything we create outside the install directory is registered here, so an
# interrupt does not leave it behind. The cookie jar used for the console API
# and the downloaded Docker installer are the only two.
TMP_FILES=()
cleanup() {
  local f
  for f in "${TMP_FILES[@]:-}"; do [ -n "$f" ] && rm -f "$f"; done
  # The `:-` and the emptiness guard together are what make an empty array safe
  # under `set -u`. The explicit return keeps a failed guard on the last
  # iteration from being this function's - and so the trap's - exit status.
  return 0
}
# Split rather than one `trap cleanup EXIT INT TERM`: cleanup returns instead of
# re-raising, so with the signals sharing the EXIT handler Ctrl-C would delete
# the temp files and then let the script carry on as though nothing had
# happened. Each signal handler clears the traps before exiting, so cleanup runs
# exactly once and the shell reports the signal it died of.
trap cleanup EXIT
trap 'cleanup; trap - INT TERM EXIT; exit 130' INT
trap 'cleanup; trap - INT TERM EXIT; exit 143' TERM

# Read one line from the terminal. Returns empty on EOF rather than failing the
# script, so a closed terminal falls through to the default.
read_line() {
  local reply=""
  if [ -n "$TTY_IN" ]; then
    IFS= read -r reply <"$TTY_IN" || reply=""
  fi
  printf '%s' "$reply"
}

# Trim leading and trailing whitespace, and nothing else. Deliberately not
# "delete every space": a token description or a display name is allowed to
# contain one, and silently eating it would be a bug the user cannot see.
trim() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "$s"
}

# ask_yes_no "question" "y|n"  -> returns 0 for yes, 1 for no
#
# The two `tr` calls run under LC_ALL=C on purpose: byte-oriented case folding
# and whitespace deletion leave every multi-byte character in the answer intact,
# so a multi-byte answer reaches the case statement below unmangled - which is
# what keeps this helper identical in the translated script, where the accepted
# answers include 是 and 否.
ask_yes_no() {
  local prompt="$1" default="$2" hint reply
  if [ "$default" = "y" ]; then hint="[Y/n]"; else hint="[y/N]"; fi
  if [ "$ASSUME_YES" = 1 ] || [ -z "$TTY_IN" ]; then
    [ "$default" = "y" ]
    return
  fi
  while true; do
    printf '    %s %s ' "$prompt" "$hint" >&2
    reply="$(read_line)"
    reply="$(printf '%s' "$reply" | LC_ALL=C tr '[:upper:]' '[:lower:]' | LC_ALL=C tr -d '[:space:]')"
    case "$reply" in
      "")    [ "$default" = "y" ]; return ;;
      y|yes) return 0 ;;
      n|no)  return 1 ;;
      *)     warn "Please answer y or n." ;;
    esac
  done
}

# ask_value "question" "default" [validator-function] -> value on stdout
ask_value() {
  local prompt="$1" default="$2" validator="${3:-}" reply
  if [ "$ASSUME_YES" = 1 ] || [ -z "$TTY_IN" ]; then
    printf '%s' "$default"
    return
  fi
  while true; do
    printf '    %s %s[%s]%s ' "$prompt" "$C_DIM" "$default" "$C_RESET" >&2
    reply="$(trim "$(read_line)")"
    [ -z "$reply" ] && reply="$default"
    if [ -z "$validator" ] || "$validator" "$reply"; then
      printf '%s' "$reply"
      return
    fi
  done
}

valid_port() {
  case "$1" in
    ''|*[!0-9]*) warn "A port is a number."; return 1 ;;
  esac
  if [ "$1" -lt 1 ] || [ "$1" -gt 65535 ]; then
    warn "A port is between 1 and 65535."
    return 1
  fi
  return 0
}

valid_path() {
  case "$1" in
    /|"") warn "That is not a directory this should install into."; return 1 ;;
    # ~ and ~/… only: choose_directory expands exactly those two forms, and a
    # ~user path accepted here would become a literal directory called ~user
    # under the current working directory.
    /*|~|~/*|./*|../*) return 0 ;;
    *) warn "Use an absolute path, for example /opt/spinneret."; return 1 ;;
  esac
}

# The server enforces ^[a-z0-9][a-z0-9._-]{2,63}$ on a username
# (internal/auth/validate.go). Rejecting it here saves a failed bootstrap.
valid_username() {
  case "$1" in
    ''|[!a-z0-9]*) warn "Start with a lower-case letter or a digit."; return 1 ;;
    *[!a-z0-9._-]*) warn "Lower-case letters, digits, dot, underscore and dash only."; return 1 ;;
  esac
  if [ "${#1}" -lt 3 ] || [ "${#1}" -gt 64 ]; then
    warn "Between 3 and 64 characters."
    return 1
  fi
  return 0
}

# An image tag ends up in .env, from there in the reference compose resolves, and
# in comparisons in this script. A registry tag is [A-Za-z0-9_][A-Za-z0-9._-]{0,127};
# anything else cannot be pulled anyway, and a value carrying ${...} would land in
# a file compose interpolates. `source` is the sentinel do_upgrade uses for the
# build-from-source path.
valid_image_tag() {
  case "$1" in
    source) return 0 ;;
    ''|[!A-Za-z0-9_]*) warn "A tag starts with a letter, a digit or an underscore."; return 1 ;;
    *[!A-Za-z0-9._-]*) warn "Letters, digits, dot, underscore and dash only."; return 1 ;;
  esac
  if [ "${#1}" -gt 128 ]; then
    warn "That is longer than a registry tag can be."
    return 1
  fi
  return 0
}

valid_replicas() {
  case "$1" in
    ''|*[!0-9]*) warn "That is a count, so a number."; return 1 ;;
  esac
  if [ "$1" -lt 1 ] || [ "$1" -gt 64 ]; then
    warn "Between 1 and 64."
    return 1
  fi
  return 0
}

# Absolute, with no trailing slash and no '..' left in it. The refusal list
# below compares against literal names, so it only means anything once the path
# it is comparing is the real one. Textual on purpose: realpath -e would require
# the directory to exist, and it does not yet.
absolute_path() {
  local path="$1"
  case "$path" in
    /*) : ;;
    *)  path="$PWD/$path" ;;
  esac
  local out=""
  local IFS=/
  local part
  # Globbing off around the loop. The expansion is unquoted on purpose so it
  # splits on the separator, and with globbing on, a path containing *, ? or
  # [...] would be matched against the filesystem instead - so a directory the
  # operator typed could silently become a different one that happens to exist.
  set -f
  for part in $path; do
    case "$part" in
      ''|'.') : ;;
      '..')   out="${out%/*}" ;;
      *)      out="$out/$part" ;;
    esac
  done
  set +f
  printf '%s' "${out:-/}"
}

have() { command -v "$1" >/dev/null 2>&1; }

# Run a command with sudo when we are not root, printing it first. Never used
# on a whole block - only on the single command that genuinely needs it.
as_root() {
  if [ "$(id -u)" = "0" ]; then
    "$@"
  else
    # Joined on spaces explicitly: the global IFS is newline and tab, so $* would
    # otherwise print each argument on a line of its own and the "exact command"
    # this promises to show would be unreadable.
    local IFS=' '
    dim "sudo $*"
    sudo "$@"
  fi
}

# --------------------------------------------------------------- detection --

OS=""          # linux | macos | wsl | unsupported
DISTRO=""      # debian, ubuntu, fedora, rhel, arch, alpine, opensuse …
DISTRO_LIKE="" # ID_LIKE from os-release
DISTRO_NAME=""
ARCH=""
CPUS=""
MEM_MIB=""

detect_os() {
  local kernel
  kernel="$(uname -s 2>/dev/null || echo unknown)"
  case "$kernel" in
    Linux)
      OS="linux"
      # WSL is Linux, but Docker there is Docker Desktop on the Windows side and
      # a package-manager install is the wrong advice.
      if grep -qiE "microsoft|wsl" /proc/version 2>/dev/null; then OS="wsl"; fi
      if [ -r /etc/os-release ]; then
        # Sourced in a subshell so os-release's variables cannot leak into this
        # script's namespace - it sets things like NAME and VERSION.
        # shellcheck disable=SC1091
        DISTRO="$(. /etc/os-release 2>/dev/null && printf '%s' "${ID:-}")"
        # shellcheck disable=SC1091
        DISTRO_LIKE="$(. /etc/os-release 2>/dev/null && printf '%s' "${ID_LIKE:-}")"
        # shellcheck disable=SC1091
        DISTRO_NAME="$(. /etc/os-release 2>/dev/null && printf '%s' "${PRETTY_NAME:-${NAME:-}}")"
      fi
      [ -n "$DISTRO_NAME" ] || DISTRO_NAME="Linux"
      ;;
    Darwin)
      OS="macos"
      DISTRO_NAME="macOS $(sw_vers -productVersion 2>/dev/null || true)"
      ;;
    FreeBSD|OpenBSD|NetBSD)
      OS="unsupported"; DISTRO_NAME="$kernel" ;;
    MINGW*|MSYS*|CYGWIN*)
      OS="unsupported"; DISTRO_NAME="Windows (Git Bash)" ;;
    *)
      OS="unsupported"; DISTRO_NAME="$kernel" ;;
  esac

  ARCH="$(uname -m 2>/dev/null || echo unknown)"
  case "$ARCH" in
    x86_64|amd64)  ARCH="x86_64" ;;
    aarch64|arm64) ARCH="arm64" ;;
  esac
}

# Which family a distribution belongs to, for the Docker install advice. The
# ID_LIKE fallback is matched space-padded, which is the idiom that makes a
# substring match on a space-separated list safe.
distro_family() {
  local id="${DISTRO}" like=" ${DISTRO_LIKE} "
  case "$id" in
    debian|ubuntu|linuxmint|pop|raspbian|kali|neon|elementary|zorin|deepin|armbian|devuan) echo debian; return ;;
    rhel|centos|rocky|almalinux|fedora|ol|oracle|amzn|scientific|circle) echo rhel; return ;;
    opensuse*|sles|sled|suse) echo suse; return ;;
    arch|manjaro|endeavouros|garuda|cachyos|artix) echo arch; return ;;
    alpine) echo alpine; return ;;
    nixos) echo nixos; return ;;
    void) echo void; return ;;
    gentoo) echo gentoo; return ;;
  esac
  case "$like" in
    *" debian "*|*" ubuntu "*) echo debian; return ;;
    *" rhel "*|*" fedora "*|*" centos "*) echo rhel; return ;;
    *" suse "*|*" opensuse "*) echo suse; return ;;
    *" arch "*) echo arch; return ;;
  esac
  echo unknown
}

detect_resources() {
  CPUS="$(
    { command -v nproc >/dev/null 2>&1 && nproc; } \
      || sysctl -n hw.ncpu 2>/dev/null \
      || getconf _NPROCESSORS_ONLN 2>/dev/null \
      || echo 1
  )"
  # Re-validated rather than trusted: a probe that printed a warning would
  # otherwise end up in arithmetic further down.
  case "$CPUS" in ''|*[!0-9]*) CPUS=1 ;; esac
  [ "$CPUS" -ge 1 ] || CPUS=1

  if [ -r /proc/meminfo ]; then
    MEM_MIB="$(awk '/^MemTotal:/ {printf "%d", $2/1024; exit}' /proc/meminfo 2>/dev/null || echo 0)"
  elif have sysctl; then
    local bytes
    bytes="$(sysctl -n hw.memsize 2>/dev/null || echo 0)"
    case "$bytes" in ''|*[!0-9]*) bytes=0 ;; esac
    MEM_MIB=$(( bytes / 1024 / 1024 ))
  else
    MEM_MIB=0
  fi
  case "$MEM_MIB" in ''|*[!0-9]*) MEM_MIB=0 ;; esac
}

# ------------------------------------------------------------------ docker --

# "2.29.7" -> 2029007, so versions compare as integers without sort -V. A
# leading v is stripped first: awk -F'[^0-9]+' on "v2.29.7" yields an empty
# first field and would report a current Compose as ancient.
version_key() {
  printf '%s' "${1#[vV]}" | awk -F'[^0-9]+' '{printf "%d%03d%03d", $1+0, $2+0, $3+0}'
}

check_docker_present() { have docker; }
check_docker_running() { docker info >/dev/null 2>&1; }

check_compose() {
  if docker compose version >/dev/null 2>&1; then
    local raw key
    raw="$(docker compose version --short 2>/dev/null || echo 0)"
    key="$(version_key "$raw")"
    # An unreadable version has to read as too old rather than as fine: `|| echo
    # 0` covers a non-zero exit but not an empty-but-successful one, and
    # `[ "" -lt N ]` exits 2 - non-zero, so the too-old branch below would be
    # skipped and the minimum-version gate would pass.
    case "$key" in ''|*[!0-9]*) key=0 ;; esac
    if [ "$key" -lt "$(version_key "$COMPOSE_MIN")" ]; then
      warn "Compose is $raw; this stack needs $COMPOSE_MIN or newer."
      info "Update Docker, then run this again. See $DOCS_INSTALL"
      return 1
    fi
    ok "Docker Compose $raw"
    return 0
  fi
  if have docker-compose; then
    warn "Found the old standalone docker-compose (v1)."
    info "This stack uses Compose v2 merge tags. Install a current Docker, which"
    info "ships Compose as a plugin: $DOCKER_INSTALL_URL"
    return 1
  fi
  warn "Docker Compose v2 is not installed."
  return 1
}

docker_install_hint() {
  case "$(distro_family)" in
    debian) printf '%s' "sudo apt-get update && sudo apt-get install -y docker.io docker-compose-v2" ;;
    rhel)   printf '%s' "sudo dnf install -y docker docker-compose-plugin  # or: yum" ;;
    suse)   printf '%s' "sudo zypper install -y docker docker-compose" ;;
    arch)   printf '%s' "sudo pacman -S --needed docker docker-compose" ;;
    alpine) printf '%s' "sudo apk add docker docker-cli-compose && sudo rc-update add docker default" ;;
    nixos)  printf '%s' "add virtualisation.docker.enable = true; to configuration.nix" ;;
    void)   printf '%s' "sudo xbps-install -S docker docker-compose" ;;
    gentoo) printf '%s' "sudo emerge app-containers/docker app-containers/docker-compose" ;;
    *)      printf '%s' "see https://docs.docker.com/engine/install/" ;;
  esac
}

pkg_install_hint() {
  local pkg="$1"
  case "$(distro_family)" in
    debian) printf '%s' "sudo apt-get install -y $pkg" ;;
    rhel)   printf '%s' "sudo dnf install -y $pkg" ;;
    suse)   printf '%s' "sudo zypper install -y $pkg" ;;
    arch)   printf '%s' "sudo pacman -S --needed $pkg" ;;
    alpine) printf '%s' "sudo apk add $pkg" ;;
    *)      printf '%s' "install $pkg with your package manager" ;;
  esac
}

# Whether Docker's official convenience script supports this distribution. It
# covers the Debian, RHEL and SUSE families; on Arch and Alpine it refuses, and
# telling somebody to pipe a script that will refuse them is worse than telling
# them the one command their package manager wants.
convenience_script_supported() {
  case "$(distro_family)" in
    debian|rhel|suse) return 0 ;;
    *) return 1 ;;
  esac
}

offer_docker_install() {
  case "$OS" in
    macos)
      die "Docker is not installed.
    On macOS install Docker Desktop or OrbStack, start it, then run this again:
      https://www.docker.com/products/docker-desktop/
    Homebrew: brew install --cask docker" ;;
    wsl)
      die "Docker is not available inside this WSL distribution.
    Install Docker Desktop on Windows and turn on WSL integration for this
    distribution, then run this again:
      https://docs.docker.com/desktop/wsl/" ;;
    unsupported)
      die "This script installs Docker only on Linux and expects Docker Desktop
    on macOS. On $DISTRO_NAME, install Docker yourself and run this again:
      https://docs.docker.com/engine/install/" ;;
  esac

  warn "Docker is not installed."
  info ""
  info "Two ways to fix that. Both need sudo."
  info ""
  info "  1. Your package manager:"
  dim  "     $(docker_install_hint)"
  if convenience_script_supported; then
    info ""
    info "  2. Docker's own installer, which this script can run for you:"
    dim  "     curl -fsSL $DOCKER_INSTALL_URL | sudo sh"
    info ""
    info "     That downloads a shell script from Docker Inc and runs it as root."
    info "     It is the official one and it is what Docker's own documentation"
    info "     tells you to use, but it is still remote code as root, so it is"
    info "     your call rather than the default."
    info ""
    if ask_yes_no "Run Docker's installer now?" "n"; then
      step "Installing Docker"
      local tmp
      tmp="$(mktemp -t spinneret-docker-install.XXXXXX)" || die "Could not create a temporary file."
      TMP_FILES+=("$tmp")
      # Download first, then run: a pipe straight into a shell cannot be
      # inspected, and a truncated download becomes a half-executed script.
      if ! curl -fsSL --proto '=https' --tlsv1.2 "$DOCKER_INSTALL_URL" -o "$tmp"; then
        rm -f "$tmp"
        die "Could not download the Docker installer. Install Docker by hand and run this again."
      fi
      if [ ! -s "$tmp" ]; then
        rm -f "$tmp"
        die "The Docker installer downloaded empty. Install Docker by hand and run this again."
      fi
      info "Downloaded to $tmp ($(wc -c <"$tmp" | tr -d ' ') bytes)."
      as_root sh "$tmp" || { rm -f "$tmp"; die "The Docker installer failed. Read its output above."; }
      rm -f "$tmp"
      ok "Docker installed."
    else
      die "Install Docker with the command above, then run this script again."
    fi
  else
    info ""
    info "  2. Docker's own installer does not support $DISTRO_NAME, so use the"
    info "     package manager command above."
    die "Install Docker, then run this script again."
  fi
}

ensure_docker_running() {
  if check_docker_running; then
    return 0
  fi

  if [ "$OS" = "macos" ]; then
    die "Docker is installed but not running. Start Docker Desktop (or OrbStack)
    and wait for it to say it is running, then run this script again."
  fi

  warn "Docker is installed but not answering."
  if have systemctl && ask_yes_no "Start the Docker service now?" "y"; then
    as_root systemctl enable --now docker || true
    sleep 2
  elif have rc-service && ask_yes_no "Start the Docker service now?" "y"; then
    as_root rc-update add docker default || true
    as_root rc-service docker start || true
    sleep 2
  fi

  if check_docker_running; then
    ok "Docker is running."
    return 0
  fi

  # The other common cause: the daemon is up but this user is not allowed to
  # talk to it. Say which of the two it is rather than "cannot connect".
  if [ "$(id -u)" != "0" ] && sudo -n docker info >/dev/null 2>&1; then
    die "Docker is running, but your user cannot reach it.
    Add yourself to the docker group and start a new login shell:
      sudo usermod -aG docker \"\$USER\"
      newgrp docker
    Then run this script again. (Group membership is the same as root access on
    this machine, which is why this script will not do it for you.)"
  fi

  die "Docker is installed but not running, and starting it did not work.
    Start it however this system does, check with 'docker info', then run this
    script again."
}

# ------------------------------------------------------------------ random --
#
# /dev/urandom first, openssl as the fallback, rather than the other way round:
# the stack's own scripts/compose-init.sh needs openssl on PATH and a standalone
# installer should not. In both helpers the byte source is the FIRST command in
# the pipeline, so the reader closing early cannot SIGPIPE a producer and trip
# `set -o pipefail`.

# Alphanumeric only. Both database passwords are interpolated raw into
# postgres://…@postgres and clickhouse://…@clickhouse URLs in the compose file,
# so anything needing percent-encoding would silently produce a wrong password.
random_alnum() {
  local want="$1" out="" chunk
  case "$want" in ''|*[!0-9]*) die "random_alnum: bad length." ;; esac
  while [ "${#out}" -lt "$want" ]; do
    if [ -r /dev/urandom ]; then
      chunk="$(LC_ALL=C head -c 512 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9' || true)"
    elif have openssl; then
      chunk="$(openssl rand -base64 384 | LC_ALL=C tr -dc 'A-Za-z0-9' || true)"
    else
      die "No source of randomness (/dev/urandom or openssl). Cannot generate secrets."
    fi
    [ -n "$chunk" ] || die "Could not read randomness. Cannot generate secrets."
    out="$out$chunk"
  done
  printf '%s' "${out:0:$want}"
}

# Base64 of N random bytes, for the KEK line. The vault requires the value to
# decode to exactly 32 bytes (internal/vault/kek.go).
random_b64() {
  local bytes="$1"
  # Each pipeline's status is returned rather than dropped. The caller checks the
  # length of what comes back as well: `die` inside a command substitution exits
  # only that subshell, so an unchecked failure here would otherwise become a
  # KEK line with no key material in it.
  if [ -r /dev/urandom ] && have base64; then
    head -c "$bytes" /dev/urandom | base64 | tr -d '\n' || return 1
  elif have openssl; then
    openssl rand -base64 "$bytes" | tr -d '\n' || return 1
  else
    die "No source of randomness (/dev/urandom or openssl). Cannot generate the KEK."
  fi
}

# ------------------------------------------------------------------- steps --
#
# Every answer can be preset from the environment, which is what makes --yes a
# complete unattended install rather than just "all defaults". Anything not set
# falls back to the value that is safe to pick for somebody who is not here.

INSTALL_DIR=""
BIND_HOST="${SPINNERET_BIND_HOST:-127.0.0.1}"
BIND_PORT="${SPINNERET_PORT:-8080}"
ADMIN_USER="${SPINNERET_ADMIN_USERNAME:-admin}"
REPLICAS="${SPINNERET_REPLICAS:-}"
WANT_OBSERVABILITY="${SPINNERET_ENABLE_OBSERVABILITY:-0}"
USE_PUBLISHED="${SPINNERET_USE_PUBLISHED:-1}"
IMAGE_REPO="${SPINNERET_IMAGE:-$DEFAULT_IMAGE}"
IMAGE_TAG="${SPINNERET_IMAGE_TAG:-latest}"
ADMIN_PASSWORD=""   # generated in write_env, shown once in show_result

banner() {
  printf '\n'
  printf '%s  Spinneret · guided install%s\n' "$C_BOLD" "$C_RESET"
  printf '%s  Docker only. Nothing is installed or changed without asking.%s\n' "$C_DIM" "$C_RESET"
  printf '\n'
}

report_host() {
  step "Looking at this machine"
  ok "$DISTRO_NAME ($ARCH)"
  if [ "$MEM_MIB" -gt 0 ]; then
    ok "$CPUS CPU $( [ "$CPUS" = 1 ] && echo core || echo cores ), $(( MEM_MIB / 1024 )).$(( (MEM_MIB % 1024) * 10 / 1024 )) GiB RAM"
  else
    ok "$CPUS CPU $( [ "$CPUS" = 1 ] && echo core || echo cores ), memory size unknown"
  fi

  case "$ARCH" in
    x86_64|arm64) : ;;
    *) die "Images are published for x86_64 and arm64 only; this is $ARCH.
    You can still build from source on this host: $DOCS_INSTALL" ;;
  esac

  # Numbers from the stack's own measurements. ClickHouse idles at about 1.2 GiB
  # whatever its caches are set to, and PostgreSQL allocates 512 MiB of shared
  # buffers at start, so 4 GiB is the floor for the stack as shipped.
  if [ "$MEM_MIB" -gt 0 ] && [ "$MEM_MIB" -lt 3800 ]; then
    warn "Under 4 GiB of RAM. The stack as shipped wants 4 GiB: ClickHouse alone"
    warn "idles at about 1.2 GiB and PostgreSQL takes 512 MiB of shared buffers."
    warn "It will still start, but a busy host will meet the OOM killer."
  fi
  if [ "$CPUS" -lt 2 ]; then
    warn "One CPU core. Expect slow first starts; two or more is the real floor."
  fi
}

choose_directory() {
  step "Where to install"
  local default_dir
  if [ -n "${SPINNERET_INSTALL_DIR:-}" ]; then
    default_dir="$SPINNERET_INSTALL_DIR"
  elif [ "$(id -u)" = "0" ]; then
    default_dir="/opt/spinneret"
  else
    default_dir="$HOME/spinneret"
  fi
  dim "The checkout, your .env, the key-encryption key and the control script"
  dim "live here. The databases live in Docker volumes, not here."
  INSTALL_DIR="$(ask_value "Install directory?" "$default_dir" valid_path)"

  # Expand a leading ~ ourselves; `read` does not expand it, so it arrives as a
  # literal character and this is a pattern match, not an expansion.
  # shellcheck disable=SC2088
  case "$INSTALL_DIR" in
    "~") INSTALL_DIR="$HOME" ;;
    "~/"*) INSTALL_DIR="$HOME/${INSTALL_DIR#\~/}" ;;
  esac
  INSTALL_DIR="$(absolute_path "$INSTALL_DIR")"

  case "$INSTALL_DIR" in
    ""|"/"|"/usr"|"/etc"|"/var"|"/bin"|"/sbin"|"/lib"|"/boot"|"/home"|"/root"|"/opt")
      die "Refusing to install into $INSTALL_DIR." ;;
  esac
}

prepare_directory() {
  if [ -e "$INSTALL_DIR" ] && [ ! -d "$INSTALL_DIR" ]; then
    die "$INSTALL_DIR exists and is not a directory."
  fi

  if [ -d "$INSTALL_DIR/.git" ]; then
    local origin
    origin="$(git -C "$INSTALL_DIR" remote get-url origin 2>/dev/null || echo "")"
    # The checkout itself is the test, not a substring of its remote URL. What
    # happens next is that .env, the key-encryption key and the compose
    # overrides are written in here and the directory is handed to
    # `docker compose build` as a build context, so what has to be true is that
    # this is a Spinneret checkout - not that some remote URL contains the word.
    if [ ! -f "$INSTALL_DIR/deploy/compose/docker-compose.yml" ] \
       || [ ! -f "$INSTALL_DIR/deploy/compose/.env.example" ]; then
      die "$INSTALL_DIR is a git checkout of something else ($origin).
    A Spinneret checkout has deploy/compose/docker-compose.yml in it.
    Choose an empty directory."
    fi
    ok "Existing checkout found at $INSTALL_DIR."
    info "Your .env, your key-encryption key and your Docker volumes are left alone."
    if ask_yes_no "Update the checkout to the latest main?" "y"; then
      git -C "$INSTALL_DIR" fetch --quiet origin main || warn "Could not fetch; using the checkout as it is."
      git -C "$INSTALL_DIR" checkout --quiet main 2>/dev/null || true
      # --ff-only, never --hard: a reset would throw away edits somebody
      # made on purpose, and this script has no business doing that.
      git -C "$INSTALL_DIR" merge --ff-only --quiet origin/main \
        || warn "The checkout has local changes; leaving it as it is."
    fi
    return
  fi

  if [ -d "$INSTALL_DIR" ] && [ -n "$(ls -A "$INSTALL_DIR" 2>/dev/null || true)" ]; then
    die "$INSTALL_DIR is not empty and is not an existing install. Choose another directory."
  fi

  step "Fetching the source"
  have git || die "git is not installed. Install it with $(pkg_install_hint git), then run this again."
  dim "The whole repository, because the fallback path builds the image from it"
  dim "and the compose build context is the repository root."

  local parent
  parent="$(dirname "$INSTALL_DIR")"
  if [ ! -d "$parent" ]; then
    # Named before it is created: this is the only thing the script makes outside
    # the directory the operator chose, and `mkdir -p` makes every level of it.
    info "$parent does not exist; creating it and every missing directory above it."
    as_root mkdir -p "$parent"
  fi
  if [ ! -w "$parent" ]; then
    as_root mkdir -p "$INSTALL_DIR"
    as_root chown "$(id -u):$(id -g)" "$INSTALL_DIR"
  fi

  git clone --quiet --depth 1 --branch main "$REPO_URL" "$INSTALL_DIR" \
    || die "Could not clone $REPO_URL. Check the network and try again: $DOCS_INSTALL"
  ok "Cloned into $INSTALL_DIR"
}

# The compose project directory. Everything the stack reads by a relative path -
# .env, ./config, ./secrets - resolves against it, and ../.. from it is the
# repository root that the build context points at.
compose_dir() { printf '%s/deploy/compose' "$INSTALL_DIR"; }

# Whether a tag of the published image can be fetched. Answered before the
# question about it, so the question can say which way it is about to go.
image_available() {
  docker manifest inspect "$1" >/dev/null 2>&1
}

ask_questions() {
  step "A few questions"

  dim "127.0.0.1 means only this machine can reach the console. The console is"
  dim "an admin surface and /metrics is on the same listener with no auth, so"
  dim "publish it on every interface only behind a reverse proxy with TLS."
  local loopback_default="y"
  [ "$BIND_HOST" = "0.0.0.0" ] && loopback_default="n"
  if ask_yes_no "Keep it on 127.0.0.1 (recommended)?" "$loopback_default"; then
    BIND_HOST="127.0.0.1"
  else
    BIND_HOST="0.0.0.0"
    warn "Publishing on 0.0.0.0. Put TLS in front of it before anyone else can route to this host."
    warn "Then set SPINNERET_COOKIE_SECURE=true in .env so session cookies are not sent in clear."
  fi
  BIND_PORT="$(ask_value "Which port?" "$BIND_PORT" valid_port)"

  printf '\n'
  dim "The first administrator of the console. The password is generated here"
  dim "and shown once at the end; nothing is sent anywhere."
  ADMIN_USER="$(ask_value "Administrator username?" "$ADMIN_USER" valid_username)"

  printf '\n'
  dim "Server replicas behind the load balancer. They are stateless. With one"
  dim "replica there is a gap during an upgrade; with two there is not, at the"
  dim "cost of roughly 150-500 MiB more RAM."
  local repl_default="$REPLICAS"
  if [ -z "$repl_default" ]; then
    # One replica per two cores, clamped: below 4 GiB a second replica competes
    # with PostgreSQL and ClickHouse for memory that is not there.
    repl_default=$(( CPUS / 2 ))
    [ "$repl_default" -lt 1 ] && repl_default=1
    [ "$repl_default" -gt 4 ] && repl_default=4
    if [ "$MEM_MIB" -gt 0 ] && [ "$MEM_MIB" -lt 3800 ]; then repl_default=1; fi
  fi
  REPLICAS="$(ask_value "How many server replicas?" "$repl_default" valid_replicas)"
  if [ "$REPLICAS" = 1 ]; then
    dim "One replica: an upgrade will have a short window with nothing serving."
  fi

  printf '\n'
  dim "The observability profile adds Prometheus scraping the server's /metrics."
  dim "It is published on 127.0.0.1 only, always: it has no authentication."
  local obs_default="n"; [ "$WANT_OBSERVABILITY" = 1 ] && obs_default="y"
  if ask_yes_no "Enable the observability profile?" "$obs_default"; then
    WANT_OBSERVABILITY=1
  else
    WANT_OBSERVABILITY=0
  fi

  printf '\n'
  local ref="$IMAGE_REPO:$IMAGE_TAG"
  if [ "$USE_PUBLISHED" = 1 ] && image_available "$ref"; then
    dim "$ref can be pulled. That takes about a minute."
    dim "Building from source instead takes 5-15 minutes and about 2 GB of"
    dim "build cache on a first build."
    if ask_yes_no "Use the published image (recommended)?" "y"; then
      USE_PUBLISHED=1
    else
      USE_PUBLISHED=0
    fi
  else
    if [ "$USE_PUBLISHED" = 1 ]; then
      warn "$ref cannot be fetched from here."
      info "Either no such tag has been published yet, or this host cannot reach"
      info "the registry. Nothing is wrong with your checkout."
      info ""
    fi
    dim "Falling back to building the image from the source in $INSTALL_DIR."
    dim "Budget 5-15 minutes and about 2 GB of build cache on a first build;"
    dim "the build needs to reach the Go and Node package registries."
    USE_PUBLISHED=0
    if [ -n "${SPINNERET_IMAGE:-}" ] || [ -n "${SPINNERET_IMAGE_TAG:-}" ]; then
      ask_yes_no "Build from source instead?" "y" || die "Nothing to install from.
    Publish or mirror $ref, or run this again and let it build from source."
    fi
  fi
}

# .env is written from deploy/compose/.env.example rather than from a block of
# printfs in here. That way every comment in the shipped example - and every
# variable added to it later - survives into the file the operator will read,
# and this script only has to know about the handful of keys it sets.
write_env() {
  local dir env_file example tmp old_umask
  dir="$(compose_dir)"
  env_file="$dir/.env"
  example="$dir/.env.example"

  step "Writing the environment"

  if [ -f "$env_file" ]; then
    ok "Keeping the existing $env_file."
    info "Its passwords are what the database volumes were built with; a new"
    info "file would not open them. Delete it yourself if you mean to start over."
    ADMIN_PASSWORD="$(env_get SPINNERET_ADMIN_PASSWORD)"
    return
  fi
  [ -f "$example" ] || die "$example is missing. The checkout in $INSTALL_DIR is incomplete."

  local pg_pass ch_pass
  pg_pass="$(random_alnum 32)"
  ch_pass="$(random_alnum 32)"
  ADMIN_PASSWORD="$(random_alnum 24)"

  # 0600 from the moment it exists, and written to a temp file in the same
  # directory then moved into place, so an interrupt cannot leave a half-written
  # .env where a whole one used to be.
  old_umask="$(umask)"
  umask 077
  # mktemp rather than a $$-derived name: the redirection below follows a
  # symlink, and a predictable name in a directory somebody else can write is
  # the one way this script could be made to write outside the install
  # directory. Same directory, so the mv at the end stays atomic.
  tmp="$(mktemp "$env_file.tmp.XXXXXX")" \
    || { umask "$old_umask"; die "Could not create a temporary file next to $env_file."; }
  TMP_FILES+=("$tmp")
  {
    printf '# Written by install/install.sh on %s.\n' "$(date -u '+%Y-%m-%d %H:%M:%SZ')"
    printf '# This file holds the database passwords and the first administrator password.\n'
    printf '# Back it up together with secrets/kek.key, which is the key everything stored\n'
    printf '# in the vault is encrypted with. Neither is recoverable if lost.\n'
    printf '\n'
    V_PG="$pg_pass" V_CH="$ch_pass" V_USER="$ADMIN_USER" V_PASS="$ADMIN_PASSWORD" \
    V_PORT="$BIND_PORT" V_REPL="$REPLICAS" awk '
      /^PG_PASSWORD=/              { print "PG_PASSWORD=" ENVIRON["V_PG"];                    seen_pg=1;   next }
      /^CLICKHOUSE_PASSWORD=/      { print "CLICKHOUSE_PASSWORD=" ENVIRON["V_CH"];            seen_ch=1;   next }
      /^SPINNERET_ADMIN_USERNAME=/ { print "SPINNERET_ADMIN_USERNAME=" ENVIRON["V_USER"];     seen_user=1; next }
      /^SPINNERET_ADMIN_PASSWORD=/ { print "SPINNERET_ADMIN_PASSWORD=" ENVIRON["V_PASS"];     seen_pass=1; next }
      /^SPINNERET_PORT=/           { print "SPINNERET_PORT=" ENVIRON["V_PORT"];               seen_port=1; next }
      /^SPINNERET_REPLICAS=/       { print "SPINNERET_REPLICAS=" ENVIRON["V_REPL"];           seen_repl=1; next }
                                   { print }
      END {
        # A key the example no longer carries still has to end up in the file:
        # compose refuses to interpolate without the two passwords at all.
        if (!seen_pg)   print "PG_PASSWORD=" ENVIRON["V_PG"]
        if (!seen_ch)   print "CLICKHOUSE_PASSWORD=" ENVIRON["V_CH"]
        if (!seen_user) print "SPINNERET_ADMIN_USERNAME=" ENVIRON["V_USER"]
        if (!seen_pass) print "SPINNERET_ADMIN_PASSWORD=" ENVIRON["V_PASS"]
        if (!seen_port) print "SPINNERET_PORT=" ENVIRON["V_PORT"]
        if (!seen_repl) print "SPINNERET_REPLICAS=" ENVIRON["V_REPL"]
      }
    ' "$example"
    printf '\n'
    printf '# --- Written by install/install.sh ---------------------------------------------------------\n'
    printf '# Address the load balancer publishes on. Read by compose.host.yml, not by the server.\n'
    printf 'SPINNERET_BIND_HOST=%s\n' "$BIND_HOST"
    if [ "$USE_PUBLISHED" = 1 ]; then
      printf '# Published image. Pinned rather than following a moving tag, so an operator can say\n'
      printf '# which build is running and put it back. Read by compose.image.yml.\n'
      printf 'SPINNERET_IMAGE=%s\n' "$IMAGE_REPO"
      printf 'SPINNERET_IMAGE_TAG=%s\n' "$IMAGE_TAG"
    else
      printf '# This install builds the image from the checkout; there is no compose.image.yml.\n'
    fi
  } >"$tmp"
  umask "$old_umask"
  chmod 600 "$tmp"
  mv -f "$tmp" "$env_file"
  ok "Wrote $env_file (owner-readable only)."
}

# The key-encryption key. Written once, never replaced, and 0644 on purpose:
# compose bind-mounts this exact file into the container, which runs as the
# distroless nonroot user (uid 65532) and matches no host user. A 0600 file is
# unreadable there and the server exits with "vault: read kek file … permission
# denied". The directory is 0700, which is where the protection belongs.
write_kek() {
  local dir kek old_umask
  dir="$(compose_dir)/secrets"
  kek="$dir/kek.key"

  mkdir -p "$dir"
  chmod 0700 "$dir" 2>/dev/null || true

  if [ -f "$kek" ]; then
    ok "Keeping the existing $kek."
    chmod 0644 "$kek" 2>/dev/null || true
    return
  fi

  # Said before the key exists, not only after it does. Under `curl | bash` the
  # comment at the top of this file is not something anybody reads, and this is
  # the one decision in the install that cannot be undone afterwards.
  printf '\n'
  warn "About to generate the key-encryption key for this deployment."
  warn "Everything the vault stores is encrypted with it, and there is no way"
  warn "back: a database restored without this file cannot be opened by anyone."
  printf '\n'

  # Generated into a variable and checked before it reaches the file: `die`
  # inside a command substitution exits only that subshell, so an unchecked
  # $(random_b64 32) can leave the literal string "k1:" behind - a KEK with no
  # key material in it, which the guard above would then keep forever.
  local key
  key="$(random_b64 32)" || key=""
  case "$key" in ''|*[!A-Za-z0-9+/=]*) key="" ;; esac
  [ "${#key}" -ge 43 ] || die "Could not generate a 32-byte key. Install openssl, or make
    /dev/urandom readable, then run this again."

  old_umask="$(umask)"
  umask 077
  printf 'k1:%s\n' "$key" >"$kek"
  umask "$old_umask"
  chmod 0644 "$kek"

  ok "Wrote $kek"
  printf '\n'
  warn "This file is the root of the secret vault."
  warn "Every identity payload, proxy URL and vault secret is encrypted with it."
  warn "A database restored without it cannot be opened, by you or by anyone."
  warn "Copy it somewhere safe before you put anything into this deployment:"
  dim  "  $kek"
  printf '\n'
}

# The build path's local tag. The shipped compose file tags the image it builds
# `spinneret:local`, a fixed name rather than a project-scoped one, so
# `docker compose -p trial build` would overwrite the tag the containers of a
# real `spinneret` stack were created from. Under any other project name the tag
# is prefixed with it, so two stacks on one host cannot take each other's image -
# and act_free_disk can tell them apart.
write_build_override() {
  local file
  file="$(compose_dir)/compose.build.yml"

  if [ "$PROJECT" = "spinneret" ]; then
    rm -f "$file"
    return
  fi

  {
    printf '# Written by install/install.sh for compose project %s.\n' "$PROJECT"
    printf '#\n'
    printf '# The shipped compose file tags what it builds spinneret:local, which is the same\n'
    printf '# name for every project on this host. This scopes it to this project, so a build\n'
    printf '# here cannot overwrite the tag another stack is running from.\n'
    printf 'services:\n'
    local svc
    for svc in migrate spinneret init-admin; do
      printf '  %s:\n' "$svc"
      printf '    image: %s-spinneret:local\n' "$PROJECT"
    done
  } >"$file"
  ok "Wrote compose.build.yml — this project builds $PROJECT-spinneret:local."
}

# The prebuilt-image override. All three services that run the server binary -
# migrate, spinneret, init-admin - share one image; overriding only one leaves
# the other two trying to build. `build: !reset null` removes the build section
# entirely rather than leaving a context that a bundle would not have.
write_image_override() {
  local file
  file="$(compose_dir)/compose.image.yml"

  if [ "$USE_PUBLISHED" != 1 ]; then
    rm -f "$file"
    write_build_override
    return
  fi
  rm -f "$(compose_dir)/compose.build.yml"

  {
    printf '# Written by install/install.sh. Delete this file to build from source instead.\n'
    printf '#\n'
    printf '# The "build: !reset null" line drops the build section, so a compose build cannot\n'
    printf '# quietly rebuild over the image that was pulled, and the repository root does not\n'
    printf '# have to exist for compose to resolve the context. It needs Compose 2.24 or newer.\n'
    printf 'services:\n'
    local svc
    for svc in migrate spinneret init-admin; do
      printf '  %s:\n' "$svc"
      # Single quotes on purpose: compose expands these, not this script.
      # shellcheck disable=SC2016
      printf '    image: ${SPINNERET_IMAGE:-%s}:${SPINNERET_IMAGE_TAG:-latest}\n' "$DEFAULT_IMAGE"
      printf '    build: !reset null\n'
    done
  } >"$file"
  ok "Wrote compose.image.yml — pinned to $IMAGE_REPO:$IMAGE_TAG."
}

# The host override: which address the published ports bind to, and memory
# ceilings on a host smaller than the stack was written for.
#
# The bind address needs an override because the shipped compose file publishes
# "${SPINNERET_PORT:-8080}:8080" with no host part. Putting "127.0.0.1:8080"
# into SPINNERET_PORT would interpolate correctly but would also break every
# other consumer of that variable, which expects a bare port.
write_host_override() {
  local file need_mem=0
  file="$(compose_dir)/compose.host.yml"
  [ "$MEM_MIB" -gt 0 ] && [ "$MEM_MIB" -lt 7800 ] && need_mem=1

  {
    printf '# Written by install/install.sh for this host: %s CPU, %s MiB RAM.\n' "$CPUS" "$MEM_MIB"
    printf '#\n'
    printf '# The "ports: !override" tag replaces the published-port list instead of appending\n'
    printf '# to it - a plain merge would leave two mappings for the same container port and\n'
    printf '# the second one would fail to bind. It needs Compose 2.24 or newer.\n'
    printf '#\n'
    printf '# Edit this file freely. Delete it to go back to the shipped defaults.\n'
    printf 'services:\n'
    printf '  lb:\n'
    printf '    ports: !override\n'
    # Single quotes on purpose: compose expands these, not this script.
    # shellcheck disable=SC2016
    printf '      - "${SPINNERET_BIND_HOST:-127.0.0.1}:${SPINNERET_PORT:-8080}:8080"\n'
    if [ "$WANT_OBSERVABILITY" = 1 ]; then
      printf '  prometheus:\n'
      printf '    # Loopback whatever the console is bound to: Prometheus has no authentication\n'
      printf '    # and the series it holds describe every namespace in the deployment.\n'
      printf '    ports: !override\n'
      # Single quotes on purpose: compose expands this, not this script.
      # shellcheck disable=SC2016
      printf '      - "127.0.0.1:${PROMETHEUS_PORT:-9090}:9090"\n'
    fi
    if [ "$need_mem" = 1 ]; then
      printf '  # Ceilings, not reservations. On a host this size the shipped components add up\n'
      printf '  # to more RAM than exists, and an unlucky moment takes the OOM killer to\n'
      printf '  # PostgreSQL rather than to whatever was actually growing.\n'
      printf '  clickhouse:\n    mem_limit: %sm\n' "$(( MEM_MIB * 2 / 5 ))"
      printf '  postgres:\n    mem_limit: %sm\n' "$(( MEM_MIB / 4 ))"
      printf '  valkey:\n    mem_limit: %sm\n' "$(( MEM_MIB / 5 ))"
      printf '  spinneret:\n    mem_limit: %sm\n' "$(( MEM_MIB / 8 ))"
    fi
  } >"$file"

  if [ "$need_mem" = 1 ]; then
    ok "Wrote compose.host.yml — bind address, and ceilings scaled to $MEM_MIB MiB."
  else
    ok "Wrote compose.host.yml — publishing on $BIND_HOST:$BIND_PORT."
  fi
}

# One entry point for every later command. It carries the four things that have
# to be right every single time and whose absence produces a confusing failure
# rather than an error: the project name, the compose files in the right order,
# the working directory that makes ./config, ./secrets and ../.. resolve, and
# the env file compose interpolates from.
write_control_script() {
  local ctl="$INSTALL_DIR/spnrctl"
  {
    printf '#!/usr/bin/env bash\n'
    printf '# The single entry point for this deployment. Written by install/install.sh.\n'
    printf '#\n'
    printf '#   ./spnrctl ps                       what is running\n'
    printf '#   ./spnrctl logs -f spinneret        follow the server log\n'
    printf '#   ./spnrctl restart lb               restart one service\n'
    printf '#   ./spnrctl up -d --wait             start, wait for healthy\n'
    printf '#   ./spnrctl down                     stop, keep the data\n'
    printf '#   ./spnrctl down -v                  stop and delete the volumes. Irreversible.\n'
    printf '#   ./spnrctl exec spinneret sh        a shell in a replica (there is none: distroless)\n'
    printf '#\n'
    printf '# Everything after the name is passed straight to docker compose, so anything\n'
    printf '# compose can do, this can do - with this deployment already selected.\n'
    printf '#\n'
    printf '# The administration CLI lives in the image as /usr/local/bin/spnr and needs only\n'
    printf '# PostgreSQL, so run it through the one-shot migrate service rather than through a\n'
    printf '# replica that may not be healthy:\n'
    printf '#\n'
    printf '#   ./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status\n'
    printf '#   ./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate token create --help\n'
    printf '#\n'
    printf '# Re-open the installer menu - status, upgrade, users, tokens, backup, uninstall:\n'
    printf '#\n'
    printf '#   bash install.sh --manage\n'
    printf 'set -euo pipefail\n'
    # Single quotes on purpose: these expand when spnrctl runs, not now. cd into
    # the compose directory rather than passing an absolute -f, so the project
    # directory is that directory and ./config, ./secrets, .env and the ../..
    # build context all resolve the way the shipped compose file expects.
    # shellcheck disable=SC2016
    printf 'cd "$(dirname "$0")/deploy/compose"\n'
    printf 'export COMPOSE_ENV_FILES=.env\n'
    printf 'exec docker compose -p %s -f docker-compose.yml' "$PROJECT"
    [ -f "$(compose_dir)/compose.image.yml" ] && printf ' -f compose.image.yml'
    [ -f "$(compose_dir)/compose.build.yml" ] && printf ' -f compose.build.yml'
    [ -f "$(compose_dir)/compose.host.yml" ] && printf ' -f compose.host.yml'
    [ "$WANT_OBSERVABILITY" = 1 ] && printf ' --profile observability'
    # shellcheck disable=SC2016
    printf ' "$@"\n'
  } >"$ctl"
  chmod 755 "$ctl"
  ok "Wrote spnrctl — use it instead of raw docker compose."
}

bring_up() {
  step "Starting the stack"
  local ctl="$INSTALL_DIR/spnrctl"

  if [ "$USE_PUBLISHED" = 1 ]; then
    info "Pulling images. The first time takes a few minutes."
    # Deliberately not `a || b | tail || die`: a pipeline takes the exit status
    # of its LAST command, so `tail` succeeding would swallow a failed pull and
    # the install would carry on with no images.
    if ! "$ctl" pull; then
      die "Could not pull the images. Nothing has been started.
    Check that $IMAGE_REPO:$IMAGE_TAG exists and that this host can
    reach the registry, or run this again and build from source. $DOCS_INSTALL"
    fi
  else
    info "Building the server image from source. Expect 5-15 minutes on a first build."
    if ! "$ctl" build; then
      die "The image failed to build. Read the output above.
    Build one service on its own to see the error without the progress stream:
      $ctl build spinneret"
    fi
  fi

  # Applied explicitly rather than relying on the depends_on chain. `migrate` is
  # a one-shot that compose can consider already satisfied when its container
  # spec has not changed, and "the schema is at whatever it was" is not a thing
  # an installer should leave to inference. The step is serialized across
  # instances by a PostgreSQL advisory lock, so running it twice is safe.
  info "Applying database migrations."
  "$ctl" run --rm migrate || die "Migrations failed. Read the output above.
    Nothing is serving yet, so nothing is half-upgraded."

  info "Bringing services up."
  "$ctl" up -d --wait || die "The stack did not come up healthy.
    Ask it what is wrong:  $ctl ps
    Then read the log:     $ctl logs --tail 100"
  ok "Containers are up."
}

# Poll /readyz until every dependency answers ok. The body names the broken one,
# so a timeout prints it rather than "not ready".
#
#   {"checks":{"catalog":"ok","hotstate":"ok","postgres":"ok","redis":"ok"},"status":"ok"}
#
# encoding/json sorts a map's keys, so the checks come out alphabetically and
# "status" comes last. This still matches on "status":"ok" rather than on the
# whole body, because which checks are reported depends on what is configured.
wait_ready() {
  local url="http://127.0.0.1:$BIND_PORT/readyz"
  local waited=0 body=""
  step "Waiting for the stack to report ready"
  dim "$url — a cold start also has to initialise PostgreSQL, let ClickHouse"
  dim "create its schema and let the first replica build its hot state."

  while [ "$waited" -lt "$READY_TIMEOUT" ]; do
    body="$(curl -fsS --max-time 5 "$url" 2>/dev/null || true)"
    case "$body" in
      *'"status":"ok"'*) ok "Ready: $body"; return 0 ;;
    esac
    sleep 3
    waited=$(( waited + 3 ))
  done

  warn "Still not ready after ${READY_TIMEOUT}s."
  if [ -n "$body" ]; then
    info "The last answer names the dependency that is not ready:"
    dim  "  $body"
    case "$body" in
      *'epoch missing'*)
        info "That one has a command: rebuild the hot state from PostgreSQL."
        dim  "  $INSTALL_DIR/spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild" ;;
      *'building'*)
        info "That one is normal on a first start; it just needs longer." ;;
    esac
  else
    info "Nothing answered at all on port $BIND_PORT."
  fi
  die "The stack is up but not ready. Nothing has been lost; look at:
      $INSTALL_DIR/spnrctl ps
      $INSTALL_DIR/spnrctl logs --tail 100 spinneret"
}

# Create the first administrator. Idempotent by design: the command short
# circuits and exits 0 when any user is already a platform admin, so re-running
# the installer over a live deployment does not disturb it.
bootstrap_admin() {
  step "Creating the first administrator"
  local ctl="$INSTALL_DIR/spnrctl"
  if ! "$ctl" --profile init run --rm init-admin; then
    die "Could not create the administrator. The stack is running and healthy;
    only this step failed. Try it again on its own:
      $ctl --profile init run --rm init-admin"
  fi
}

show_result() {
  local ctl="$INSTALL_DIR/spnrctl" shown_host="$BIND_HOST"
  [ "$shown_host" = "0.0.0.0" ] && shown_host="127.0.0.1"
  step "Done"

  printf '\n'
  printf '    %sConsole%s     %shttp://%s:%s%s\n' "$C_BOLD" "$C_RESET" "$C_GREEN" "$shown_host" "$BIND_PORT" "$C_RESET"
  printf '    %sUsername%s    %s\n' "$C_BOLD" "$C_RESET" "$ADMIN_USER"
  printf '    %sPassword%s    %s\n' "$C_BOLD" "$C_RESET" "${ADMIN_PASSWORD:-<see SPINNERET_ADMIN_PASSWORD in .env>}"
  dim "Change it after the first sign-in. Until you do, it also sits in .env in clear."

  printf '\n'
  info "Directory   $INSTALL_DIR"
  info "Control     $ctl ps | logs -f spinneret | restart lb | down"
  info "Menu        bash install.sh --manage"
  info "Environment $(compose_dir)/.env  (0600 — the database passwords)"
  info "Vault key   $(compose_dir)/secrets/kek.key  (back this up; nothing decrypts without it)"

  printf '\n'
  info "Next, from this machine:"
  dim  "  # a token for one crawler node"
  dim  "  $ctl run --rm -T --entrypoint /usr/local/bin/spnr migrate \\"
  dim  "      token create --name node-1 --scope lease:acquire --scope report:write --scope config:read"
  dim  ""
  dim  "  # then point the node at the server"
  dim  "  export SPINNERET_URL=http://$shown_host:$BIND_PORT"
  dim  "  export SPINNERET_TOKEN=<the token printed above>"

  printf '\n'
  info "Quickstart  $DOCS_EN"
  info "快速开始    $DOCS_ZH"

  if [ "$WANT_OBSERVABILITY" = 1 ]; then
    printf '\n'
    info "Prometheus  http://127.0.0.1:${PROMETHEUS_PORT:-9090}  (loopback only, no authentication)"
  fi
  if [ "$BIND_HOST" = "0.0.0.0" ]; then
    printf '\n'
    warn "This console is published on every interface without TLS, and /metrics"
    warn "is on the same listener with no authentication. Put a reverse proxy in"
    warn "front of it before it is reachable from anywhere else."
  fi
  printf '\n'
  warn "Back up $(compose_dir)/secrets/kek.key now, before there is data to lose."
  printf '\n'
}

# ----------------------------------------------------------------- manage --
#
# Everything below runs against an install that already exists. One script for
# both jobs on purpose: somebody who deployed with it six months ago should not
# have to learn `docker compose` to reset a password.
#
# Every action delegates to spnrctl, to the `spnr` CLI inside the image, or to
# the console's own API. Nothing here reimplements what the project already
# does, so a menu entry cannot drift away from the command it stands for.

CTL=""          # path to the spnrctl of the install being managed
API_TENANT=""   # tenant id, discovered at login
API_COOKIES=""  # session cookie jar, a temp file
API_USER=""     # credentials for the console API. Globals rather than arguments
API_PASSWORD="" # because argv is readable by every process on this host.

# Where an existing deployment lives, asked of Docker rather than guessed, so it
# is found wherever it was put. Compose records the config files a project was
# started from; the first of ours is <dir>/deploy/compose/docker-compose.yml.
#
# Parsed with awk rather than python3: the reference installer this one follows
# depends on python3 without declaring it, and on a host without it the failure
# is silent - the menu never opens and the instance quietly never updates.
find_existing_install() {
  local config dir
  config="$(docker compose ls --all --format json 2>/dev/null \
    | awk -v want="$PROJECT" '
        BEGIN { RS = "}" }
        $0 ~ "\"Name\":\"" want "\"" {
          if (match($0, /"ConfigFiles":"[^"]*"/)) {
            files = substr($0, RSTART + 15, RLENGTH - 16)
            split(files, parts, ",")
            print parts[1]
            exit
          }
        }' || true)"
  [ -n "$config" ] || return 1
  # <dir>/deploy/compose/docker-compose.yml -> <dir>: three levels up.
  dir="$(dirname "$(dirname "$(dirname "$config")")")"
  [ -f "$dir/deploy/compose/docker-compose.yml" ] || return 1
  [ -x "$dir/spnrctl" ] || return 1
  printf '%s' "$dir"
}

# Read one key out of the install's .env. Everything in manage mode reads the
# deployment's own settings back rather than using this script's defaults: an
# install created with one replica and no observability profile must not be
# upgraded as though it had two and the profile on.
env_get() {
  local file
  file="$(compose_dir)/.env"
  [ -f "$file" ] || return 0
  awk -v key="$1" '
    index($0, key "=") == 1 { print substr($0, length(key) + 2); found = 1; exit }
    END { if (!found) exit 0 }
  ' "$file"
}

# Replace one key in .env, atomically. The value travels through the
# environment, not through awk -v, because awk -v processes backslash escapes
# and a password is allowed to contain one.
#
# Written to a temp file in the same directory and moved into place. The
# alternative - sed to a temp, then `cat tmp > .env` - truncates first, and an
# interrupt or a full disk in that window destroys the one file this script
# spends its success screen telling you to back up.
env_set() {
  local key="$1" value="$2" file tmp old_umask
  file="$(compose_dir)/.env"
  [ -f "$file" ] || return 1
  old_umask="$(umask)"
  umask 077
  # mktemp rather than a $$-derived name: the redirection below follows a
  # symlink, and a predictable name is the one way this script could be made to
  # write outside the install directory. Same directory, so the mv stays atomic.
  tmp="$(mktemp "$file.tmp.XXXXXX")" || { umask "$old_umask"; return 1; }
  TMP_FILES+=("$tmp")
  # The write is checked before the move. Every caller invokes this in a
  # condition context, which disables errexit for the whole function body, so an
  # unchecked awk plus an unconditional mv would truncate .env on a full disk or
  # a read error - and still return 0.
  if ! V_NEW="$value" awk -v key="$key" '
    index($0, key "=") == 1 && !done { print key "=" ENVIRON["V_NEW"]; done = 1; next }
    { print }
    END { if (!done) print key "=" ENVIRON["V_NEW"] }
  ' "$file" >"$tmp"; then
    umask "$old_umask"
    rm -f "$tmp"
    return 1
  fi
  umask "$old_umask"
  if [ ! -s "$tmp" ]; then
    rm -f "$tmp"
    return 1
  fi
  chmod 600 "$tmp"
  mv -f "$tmp" "$file" || return 1
}

# Re-derive this install's settings from its own files. Called before anything
# in manage mode touches the stack.
load_install_settings() {
  local port replicas bind image tag
  port="$(env_get SPINNERET_PORT)";        [ -n "$port" ] && BIND_PORT="$port"
  replicas="$(env_get SPINNERET_REPLICAS)"; [ -n "$replicas" ] && REPLICAS="$replicas"
  bind="$(env_get SPINNERET_BIND_HOST)";   [ -n "$bind" ] && BIND_HOST="$bind"
  # The image reference is validated where it is read, not where it is used:
  # .env is a file an operator edits, and both halves end up in a docker
  # reference and in comparisons inside this script. A value that is not a
  # plausible reference is ignored in favour of the default.
  image="$(env_get SPINNERET_IMAGE)"
  case "$image" in
    ''|*[!A-Za-z0-9._/:-]*) : ;;
    *) IMAGE_REPO="$image" ;;
  esac
  tag="$(env_get SPINNERET_IMAGE_TAG)"
  case "$tag" in
    ''|*[!A-Za-z0-9._-]*) : ;;
    *) IMAGE_TAG="$tag" ;;
  esac
  ADMIN_USER="$(env_get SPINNERET_ADMIN_USERNAME)"; [ -n "$ADMIN_USER" ] || ADMIN_USER="admin"
  # The image path is a fact on disk, not a preference: the override file either
  # exists or the stack builds from source.
  if [ -f "$(compose_dir)/compose.image.yml" ]; then USE_PUBLISHED=1; else USE_PUBLISHED=0; fi
  if grep -q -- '--profile observability' "$INSTALL_DIR/spnrctl" 2>/dev/null; then
    WANT_OBSERVABILITY=1
  else
    WANT_OBSERVABILITY=0
  fi
  case "$BIND_PORT" in ''|*[!0-9]*) BIND_PORT=8080 ;; esac
  case "$REPLICAS" in ''|*[!0-9]*) REPLICAS=1 ;; esac
}

# Run the administration CLI. Through the one-shot migrate service, which
# already carries the full SPINNERET_* environment and the KEK secret, has
# `entrypoint: []` so the image's server entrypoint is out of the way, and needs
# only PostgreSQL - so it works when no replica is healthy.
spnr_run() {
  "$CTL" run --rm -T --entrypoint /usr/local/bin/spnr migrate "$@"
}

# The version the running instance reports. The only one that counts: the
# checkout on disk can be ahead of the image that is actually serving.
running_version() {
  "$CTL" exec -T spinneret /usr/local/bin/spnr version 2>/dev/null \
    | tr -d '\r' | awk 'NF{print $NF}' | tail -1
}

latest_release() {
  local body
  body="$(curl -fsSL --proto '=https' --tlsv1.2 -m 10 "$REPO_API/releases/latest" 2>/dev/null || true)"
  [ -n "$body" ] || return 0
  printf '%s' "$body" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
}

# True when $1 is newer than $2. Numeric segments are compared left to right
# zero-padded to equal length; a pre-release suffix makes a version older than
# the same numbers without one, so v1.2.0-rc1 is behind v1.2.0.
#
# In awk rather than python3, for the same reason find_existing_install is.
version_is_newer() {
  [ -n "${1:-}" ] && [ -n "${2:-}" ] || return 1
  V_A="$1" V_B="$2" awk '
    function split_ver(raw, nums, rest,   s, head, i, parts, n) {
      s = raw
      sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); sub(/^[vV]/, "", s)
      if (match(s, /^[0-9]+(\.[0-9]+)*/)) {
        head = substr(s, 1, RLENGTH)
        rest[0] = substr(s, RLENGTH + 1)
      } else {
        head = "0"
        rest[0] = s
      }
      sub(/^[.\-_]/, "", rest[0])
      n = split(head, parts, ".")
      for (i = 1; i <= n; i++) nums[i] = parts[i] + 0
      return n
    }
    BEGIN {
      na = split_ver(ENVIRON["V_A"], a, ra)
      nb = split_ver(ENVIRON["V_B"], b, rb)
      w = (na > nb) ? na : nb
      for (i = 1; i <= w; i++) {
        x = (i <= na) ? a[i] : 0
        y = (i <= nb) ? b[i] : 0
        if (x != y) exit (x > y) ? 0 : 1
      }
      if (ra[0] == rb[0]) exit 1
      if (ra[0] == "") exit 0
      if (rb[0] == "") exit 1
      exit (ra[0] > rb[0]) ? 0 : 1
    }'
}

ask_choice() {
  local prompt="$1" reply
  printf '\n    %s ' "$prompt" >&2
  reply="$(read_line)"
  printf '%s' "$(printf '%s' "$reply" | LC_ALL=C tr -d '[:space:]' | LC_ALL=C tr '[:upper:]' '[:lower:]')"
}

# Read a password twice without echoing it. The caller keeps it in a variable
# and hands it to curl through stdin; it is never an argument, because argv is
# readable by every process on the host. Ten characters is the server's own
# minimum (internal/auth/password.go).
read_password_twice() {
  local first second
  [ -n "$TTY_IN" ] || { warn "No terminal to read a password from."; return 1; }
  while true; do
    printf '    New password (at least 10 characters): ' >&2
    IFS= read -rs first <"$TTY_IN" || first=""
    printf '\n' >&2
    if [ -z "$first" ]; then
      warn "Cancelled."
      return 1
    fi
    if [ "${#first}" -lt 10 ]; then
      warn "Too short."
      continue
    fi
    printf '    Again: ' >&2
    IFS= read -rs second <"$TTY_IN" || second=""
    printf '\n' >&2
    if [ "$first" != "$second" ]; then
      warn "They do not match."
      continue
    fi
    printf '%s' "$first"
    return 0
  done
}

read_password_once() {
  local prompt="$1" value
  [ -n "$TTY_IN" ] || return 1
  printf '    %s' "$prompt" >&2
  IFS= read -rs value <"$TTY_IN" || value=""
  printf '\n' >&2
  printf '%s' "$value"
}

# A destructive action asks for a word to be typed. A y/n in the same rhythm as
# five other y/n answers is not a decision. The word stays English in the
# translated script too: it is a token, not prose.
confirm_word() {
  local word="$1" reply
  printf '    Type %s to confirm, anything else to cancel: ' "$word" >&2
  # Trimmed like every other prompt in this script: a trailing space silently
  # cancelling a destructive action is worse than accepting one.
  reply="$(trim "$(read_line)")"
  [ "$reply" = "$word" ]
}

pause_for_reader() {
  [ -n "$TTY_IN" ] || return 0
  printf '\n    Press enter to go back. ' >&2
  read_line >/dev/null
}

# ------------------------------------------------------------ console api --
#
# Three things the `spnr` CLI deliberately does not do - reset a password, add a
# user, list users - exist only as console RPCs. They are reached here over the
# same Connect-over-JSON endpoint the console itself uses, on loopback.
#
# Passwords go into the request body, which lives in a shell variable and
# reaches curl on stdin. Never a command-line argument.

json_escape() {
  printf '%s' "$1" | LC_ALL=C sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/\t/\\t/g' | tr -d '\n\r'
}

api_url() { printf 'http://127.0.0.1:%s/spinneret.v1.%s/%s' "$BIND_PORT" "$1" "$2"; }

# api_call SERVICE METHOD <<< json  -> response body on stdout
api_call() {
  local service="$1" method="$2" body
  body="$(cat)"
  local args=(-fsS --max-time 30
    -c "$API_COOKIES" -b "$API_COOKIES"
    -X POST "$(api_url "$service" "$method")"
    -H 'Content-Type: application/json'
    -H 'X-Spinneret-CSRF: 1')
  [ -n "$API_TENANT" ] && args+=(-H "X-Spinneret-Tenant: $API_TENANT")
  printf '%s' "$body" | curl "${args[@]}" --data-binary @-
}

# Sign in with API_USER / API_PASSWORD and remember the session and the tenant.
# The tenant header carries an id, not a name; the login response is the only
# place to learn it, and tenant ids are prefixed `ten_` (internal/pkg/idgen).
api_login() {
  local body response
  API_TENANT=""
  # Also re-created when the file is gone rather than only when the variable is
  # empty: the jar lives in a shared /tmp, and the `: >` truncation below would
  # otherwise recreate a deleted one under the ambient umask, skipping the
  # chmod - with an authenticated session cookie about to be written into it.
  if [ -z "$API_COOKIES" ] || [ ! -f "$API_COOKIES" ]; then
    API_COOKIES="$(mktemp -t spinneret-session.XXXXXX)" || { warn "Could not create a temporary file."; return 1; }
    TMP_FILES+=("$API_COOKIES")
    chmod 600 "$API_COOKIES" 2>/dev/null || true
  fi
  : >"$API_COOKIES"
  body="$(printf '{"username":"%s","password":"%s"}' "$(json_escape "$API_USER")" "$(json_escape "$API_PASSWORD")")"
  if ! response="$(printf '%s' "$body" | api_call AuthService Login)"; then
    warn "Sign-in failed for $API_USER. Wrong password, or the console is not answering."
    return 1
  fi
  API_TENANT="$(printf '%s' "$response" | grep -oE 'ten_[A-Za-z0-9_-]+' | head -1 || true)"
  return 0
}

# Ask for the credentials the console actions need. The recorded password in
# .env is offered as the default so the common case is two keystrokes, but it is
# never echoed.
api_prompt_credentials() {
  local recorded
  API_USER="$(ask_value "Administrator username?" "${ADMIN_USER:-admin}")"
  recorded="$(env_get SPINNERET_ADMIN_PASSWORD)"
  if [ -n "$recorded" ]; then
    API_PASSWORD="$(read_password_once "Password (empty = the one recorded in .env): " || true)"
    [ -n "$API_PASSWORD" ] || API_PASSWORD="$recorded"
  else
    API_PASSWORD="$(read_password_once "Password: " || true)"
  fi
  [ -n "$API_PASSWORD" ] || { warn "No password given."; return 1; }
  api_login
}

# Pull "<username>\t<user id>\t<flags>" out of a ListUsers response.
#
# Split on '{' first so each protobuf message's scalar fields land on one line.
# That keeps this to POSIX awk - a multi-character RS is a regex in gawk but not
# reliably anywhere else, and an installer should not need gawk.
parse_users() {
  tr '{' '\n' | awk '
    /"id":"usr_/ && /"username":"/ {
      id = ""; user = ""; flags = ""
      if (match($0, /"id":"usr_[^"]*"/))    id   = substr($0, RSTART + 6,  RLENGTH - 7)
      if (match($0, /"username":"[^"]*"/))  user = substr($0, RSTART + 12, RLENGTH - 13)
      if ($0 ~ /"isPlatformAdmin":true/ || $0 ~ /"is_platform_admin":true/) flags = "platform-admin"
      if ($0 ~ /"disabled":true/) flags = (flags == "") ? "disabled" : flags " disabled"
      if (user != "") printf "%-28s %-30s %s\n", user, id, flags
    }'
}

# --------------------------------------------------------------- actions --

show_status() {
  step "Status"
  local installed latest
  installed="$(running_version || true)"
  if [ -n "$installed" ]; then
    ok "Running version $installed"
  else
    warn "Nothing is running, or no replica is answering."
  fi
  if [ "$USE_PUBLISHED" = 1 ]; then
    ok "Image $IMAGE_REPO:$IMAGE_TAG"
  else
    ok "Built from the checkout in $INSTALL_DIR"
  fi
  ok "Published on $BIND_HOST:$BIND_PORT, $REPLICAS replica$( [ "$REPLICAS" = 1 ] || printf s )"

  printf '\n'
  "$CTL" ps --format "table {{.Service}}\t{{.Status}}\t{{.Ports}}" 2>/dev/null | sed 's/^/    /' || true

  printf '\n'
  info "Schema:"
  spnr_run migrate status 2>&1 | sed 's/^/      /' || warn "Could not read the schema version."

  printf '\n'
  info "Disk used by Docker:"
  docker system df 2>/dev/null | sed 's/^/      /' || true

  latest="$(latest_release)"
  # Straight off the network, so checked before it is shown or offered.
  case "$latest" in ''|[!A-Za-z0-9_]*|*[!A-Za-z0-9._-]*) latest="" ;; esac
  if [ -n "$latest" ] && [ -n "$installed" ] && version_is_newer "$latest" "$installed"; then
    printf '\n'
    warn "$latest is out; this instance is on $installed."
  elif [ -n "$latest" ]; then
    printf '\n'
    ok "Up to date with the latest release ($latest)."
  fi
}

# A release is tagged v1.2.3 and the image it produces is tagged v1.2.3 as well,
# but an operator who types the release name with the v stripped should still
# get what they meant, and both spellings are accepted here.
resolve_image_ref() {
  printf '%s:%s' "$IMAGE_REPO" "$1"
}

# Replace the server replicas one container at a time. Compose recreates every
# replica of a scaled service together, which with two replicas is a window of
# five to ten seconds where nothing new starts; the load balancer's retries
# cover it, but they do not have to be spent. Removing one container and then
# running `up --no-recreate` fills the empty slot from the new spec and leaves
# the other containers alone, so one replica is always serving.
#
# It falls back to a plain recreate the moment anything about that does not
# work: a slower upgrade is better than a clever one that half-finishes.
rolling_restart() {
  local ids id count
  ids="$("$CTL" ps -q spinneret 2>/dev/null || true)"
  count="$(printf '%s\n' "$ids" | grep -c . || true)"
  case "$count" in ''|*[!0-9]*) count=0 ;; esac

  if [ "$count" -lt 2 ]; then
    [ "$count" = 1 ] && dim "One replica, so this is a restart with a gap, not a rolling one."
    "$CTL" up -d --wait || return 1
    return 0
  fi

  info "Rolling $count replicas one at a time."
  for id in $ids; do
    info "Replacing ${id:0:12}"
    docker rm -f "$id" >/dev/null 2>&1 || true
    if ! "$CTL" up -d --no-recreate --wait --no-deps spinneret; then
      warn "Could not replace that replica on its own; recreating the service instead."
      "$CTL" up -d --wait || return 1
      return 0
    fi
  done
  # Everything else - the load balancer, Prometheus - in one pass at the end.
  "$CTL" up -d --wait || return 1
}

do_upgrade() {
  local target="$1"
  # Checked here as well as at the prompt: the other source of this value is the
  # tag from the releases API, which comes off the network.
  valid_image_tag "$target" || { warn "That is not a usable image tag; nothing was changed."; return 0; }
  step "Upgrading to $target"
  info "Your data is untouched: the named volumes survive this."
  dim  "Back up first if you have not lately — menu entry 10 does it in one step."
  printf '\n'

  if [ "$USE_PUBLISHED" = 1 ]; then
    # Pull BEFORE pinning. Writing the tag first and then failing to fetch it
    # leaves the deployment pointing at an image that does not exist: the stack
    # keeps running on what is already up, but the next `up -d` cannot start.
    # The tag goes on this pull's environment only; .env is not touched until
    # the image is known to be there.
    info "Pulling $(resolve_image_ref "$target")."
    if ! SPINNERET_IMAGE_TAG="$target" "$CTL" pull migrate spinneret; then
      die "Could not pull $(resolve_image_ref "$target"). Nothing was changed and the
    old version is still running. Check the tag exists:
      https://github.com/TikHub/Spinneret/pkgs/container/spinneret"
    fi
    env_set SPINNERET_IMAGE_TAG "$target" || warn "Could not pin the tag in .env."
    IMAGE_TAG="$target"
    ok "Pinned SPINNERET_IMAGE_TAG=$target"
  else
    info "Updating the checkout."
    git -C "$INSTALL_DIR" fetch --quiet origin main || warn "Could not fetch; using the checkout as it is."
    git -C "$INSTALL_DIR" merge --ff-only --quiet origin/main 2>/dev/null \
      || warn "The checkout has local changes; leaving it alone."
    info "Rebuilding. Expect several minutes."
    "$CTL" build || die "The rebuild failed. Nothing was swapped; the old containers are still up."
  fi

  info "Migrating."
  "$CTL" run --rm migrate \
    || die "Migrations failed. The old containers are still up and nothing was swapped.
    Roll back by restoring the backup, not with 'migrate down' — a down
    migration drops tables and the data in them."

  info "Restarting."
  rolling_restart || die "The new version did not come up healthy. Try: $CTL logs --tail 100 spinneret"

  printf '\n'
  spnr_run migrate status 2>&1 | sed 's/^/    /' || true
  ok "Now on $(running_version || echo "$target")."
}

act_change_password() {
  step "Change an administrator password"
  dim "This signs in as the account and changes its own password, which is what"
  dim "the console does. Every other session of that account is ended."
  printf '\n'
  api_prompt_credentials || return 0

  local new_password body
  new_password="$(read_password_twice)" || return 0
  body="$(printf '{"currentPassword":"%s","newPassword":"%s"}' \
    "$(json_escape "$API_PASSWORD")" "$(json_escape "$new_password")")"
  if printf '%s' "$body" | api_call AuthService ChangePassword >/dev/null; then
    ok "Password changed for $API_USER."
    # SPINNERET_ADMIN_PASSWORD is only read by the one-shot init-admin, but a
    # stale value there is a trap for the next person who reads the file.
    if [ "$API_USER" = "$(env_get SPINNERET_ADMIN_USERNAME)" ]; then
      printf '\n'
      dim "The .env still records the old password for this account."
      if ask_yes_no "Update SPINNERET_ADMIN_PASSWORD in .env to match?" "y"; then
        if env_set SPINNERET_ADMIN_PASSWORD "$new_password"; then
          ok "Updated .env."
        else
          warn "Could not update .env; it still has the old value."
        fi
      else
        warn "Leaving the old value in .env. It is now wrong."
      fi
    fi
  else
    warn "That did not work. The new password must be at least 10 characters."
  fi
  unset new_password
  API_PASSWORD=""
}

act_add_admin() {
  step "Add an administrator"
  dim "Creates a console account with the admin role in the active tenant."
  dim "There is no rename: accounts are created and their passwords reset."
  printf '\n'
  api_prompt_credentials || return 0
  if [ -z "$API_TENANT" ]; then
    warn "Could not work out which tenant to create the account in."
    info "Create it from the console instead: Access → Users → New."
    return 0
  fi

  local who new_password body
  who="$(ask_value "New account name?" "" valid_username)"
  [ -n "$who" ] || { warn "No name given."; return 0; }
  new_password="$(read_password_twice)" || return 0
  body="$(printf '{"username":"%s","displayName":"%s","password":"%s","role":"admin"}' \
    "$(json_escape "$who")" "$(json_escape "$who")" "$(json_escape "$new_password")")"
  if printf '%s' "$body" | api_call AccessAdminService CreateUser >/dev/null; then
    ok "Created $who with the admin role."
    dim "Admin is tenant-wide, not platform-wide: it does not manage tenants or"
    dim "key-encryption keys. Grant that from the console if it is wanted."
  else
    warn "That did not work. The name may be taken, or the password too short."
  fi
  unset new_password
  API_PASSWORD=""
}

act_list_users() {
  step "Accounts"
  api_prompt_credentials || return 0
  local response parsed
  response="$(printf '{"allUsers":true,"pageSize":200}' | api_call AccessAdminService ListUsers || true)"
  API_PASSWORD=""
  if [ -z "$response" ]; then
    warn "Could not list accounts."
    return 0
  fi
  parsed="$(printf '%s' "$response" | parse_users || true)"
  if [ -n "$parsed" ]; then
    printf '\n'
    printf '    %-28s %-30s %s\n' "USERNAME" "ID" "FLAGS"
    printf '%s\n' "$parsed" | sed 's/^/    /'
  else
    warn "No accounts parsed out of the answer. The raw response:"
    # head last, and the whole pipeline guarded. With `head -c` in the middle it
    # closes the pipe on printf, and that SIGPIPE under pipefail and errexit
    # would take the management session down on exactly the error path this
    # branch exists to serve.
    printf '%s\n' "$response" | sed 's/^/    /' | head -c 2000 || true
    printf '\n'
  fi
}

act_create_token() {
  step "Create an API token"
  dim "A node token. Only the plaintext is printed, once — it cannot be fetched"
  dim "again, so copy it now. Scopes, comma separated:"
  dim "  lease:acquire[:<site>]  report:write[:<site>]  config:read[:<group glob>]"
  dim "  config:publish[:<group glob>]  secret:read:<namespace>/<path glob>"
  dim "  identity:write[:<site>]  proxy:write  admin"
  printf '\n'

  local name tenant namespace scopes expires description
  name="$(ask_value "Token name (unique in the namespace)?" "")"
  [ -n "$name" ] || { warn "No name given."; return 0; }
  tenant="$(ask_value "Tenant?" "default")"
  namespace="$(ask_value "Namespace?" "default")"
  scopes="$(ask_value "Scopes?" "lease:acquire,report:write,config:read")"
  expires="$(ask_value "Lifetime (720h, 30d, or never)?" "720h")"
  description="$(ask_value "Description (optional)?" "")"

  local args=(token create --tenant "$tenant" --namespace "$namespace" --name "$name" --expires "$expires")
  [ -n "$description" ] && args+=(--description "$description")
  local scope rest="$scopes"
  while [ -n "$rest" ]; do
    scope="${rest%%,*}"
    if [ "$scope" = "$rest" ]; then rest=""; else rest="${rest#*,}"; fi
    scope="$(trim "$scope")"
    [ -n "$scope" ] && args+=(--scope "$scope")
  done

  printf '\n'
  if spnr_run "${args[@]}"; then
    printf '\n'
    dim "That line above is the token. It is not stored anywhere you can read it."
  else
    warn "The token was not created. The output above says why."
  fi
}

act_rebuild() {
  step "Rebuild the hot state"
  dim "Rebuilds the Redis working set from PostgreSQL. Needed after the Valkey"
  dim "volume is recreated or flushed, which shows up as /readyz reporting"
  dim "\"hotstate: epoch missing (rebuild pending)\"."
  printf '\n'
  warn "Without a site, this deletes the epoch key first, so every replica"
  warn "reports not-ready until it finishes — a fleet-wide outage while it runs."
  warn "Naming one site avoids that and only rebuilds that site."
  printf '\n'
  local site
  site="$(ask_value "Which site (empty = the whole fleet)?" "")"
  if [ -z "$site" ]; then
    confirm_word "rebuild" || { info "Cancelled."; return 0; }
    spnr_run rebuild 2>&1 | sed 's/^/    /' || warn "The rebuild failed. Read the output above."
    return 0
  fi
  local tenant namespace
  tenant="$(ask_value "Tenant?" "default")"
  namespace="$(ask_value "Namespace?" "default")"
  spnr_run rebuild --tenant "$tenant" --namespace "$namespace" --site "$site" 2>&1 | sed 's/^/    /' \
    || warn "The rebuild failed. Read the output above."
}

act_show_config() {
  step "Configuration"
  dim "The SPINNERET_* environment the server would load, validated, with every"
  dim "credential replaced. This is the pre-flight for a hand-edited .env."
  printf '\n'
  spnr_run config check 2>&1 | sed 's/^/    /' || warn "The configuration did not validate. Each error names its variable."
}

act_diagnose() {
  step "Health check"
  local live ready

  live="$(curl -fsS --max-time 5 "http://127.0.0.1:$BIND_PORT/healthz" 2>/dev/null || true)"
  if [ -n "$live" ]; then ok "liveness  $live"; else warn "liveness  nothing answered on port $BIND_PORT"; fi

  ready="$(curl -sS --max-time 5 "http://127.0.0.1:$BIND_PORT/readyz" 2>/dev/null || true)"
  case "$ready" in
    *'"status":"ok"'*)       ok   "readiness $ready" ;;
    *'"status":"draining"'*) warn "readiness $ready  — a replica is restarting; normal for about five seconds" ;;
    "")                      warn "readiness nothing answered" ;;
    *)                       warn "readiness $ready" ;;
  esac
  case "$ready" in
    *'epoch missing'*) info "  Fix: menu entry 5, rebuild the hot state." ;;
    *'postgres'*'unreachable'*) info "  Fix: $CTL logs --tail 40 postgres" ;;
    *'building'*) info "  Normal right after a start or a rebuild; wait." ;;
  esac

  printf '\n'
  "$CTL" ps --format "table {{.Service}}\t{{.Status}}" 2>/dev/null | sed 's/^/    /' || true

  # The one failure mode on a small host that the container status does not
  # explain: a service that keeps restarting because it was OOM-killed.
  printf '\n'
  local svc cid killed
  for svc in clickhouse valkey postgres; do
    cid="$("$CTL" ps -q "$svc" 2>/dev/null | head -1 || true)"
    [ -n "$cid" ] || continue
    killed="$(docker inspect --format '{{.State.OOMKilled}}' "$cid" 2>/dev/null || echo false)"
    if [ "$killed" = "true" ]; then
      warn "$svc was OOM-killed. This host does not have the RAM the stack wants."
    else
      ok "$svc has not been OOM-killed."
    fi
  done
}

act_logs() {
  step "Logs"
  dim "Last 60 lines of each service. Follow them live with: $CTL logs -f spinneret"
  printf '\n'
  "$CTL" logs --tail 60 2>&1 | tail -120 || true
}

act_restart() {
  step "Restart"
  if ask_yes_no "Roll the server replicas one at a time?" "y"; then
    rolling_restart || warn "Restart failed."
  else
    "$CTL" restart || warn "Restart failed."
  fi
  printf '\n'
  "$CTL" ps --format "table {{.Service}}\t{{.Status}}" 2>/dev/null | sed 's/^/    /' || true
}

# A backup is three things, and it is only a backup if it is all three: the
# PostgreSQL dump, the key-encryption key that decrypts what is in it, and the
# .env whose passwords match the volumes. Valkey is derived from PostgreSQL and
# comes back with `spnr rebuild`; ClickHouse holds analytics that expire anyway.
act_backup() {
  step "Back up"
  local dir stamp target old_umask
  dir="$INSTALL_DIR/backups"
  stamp="$(date -u '+%Y%m%dT%H%M%SZ')"
  target="$dir/$stamp"
  mkdir -p "$target" || { warn "Could not create $target."; return 0; }

  dim "Into $target — inside the install directory, so a directory-level"
  dim "uninstall takes it with everything else. Copy it off this host."
  printf '\n'

  old_umask="$(umask)"
  umask 077
  info "Dumping PostgreSQL."
  if ! "$CTL" exec -T postgres pg_dump -U spinneret -Fc spinneret >"$target/postgres.dump"; then
    umask "$old_umask"
    warn "The dump failed. Read the output above."
    rm -rf -- "$target"
    return 0
  fi
  umask "$old_umask"

  # 0600 on the copy. act_restore installs it back with an explicit
  # `install -m 0644`, which sets the destination's mode whatever the source's
  # was, so nothing downstream needs this copy to be readable by anyone else -
  # and this one lands in a backups tree that gets copied around.
  install -m 0600 "$(compose_dir)/secrets/kek.key" "$target/kek.key" 2>/dev/null \
    || warn "Could not copy kek.key. The dump alone cannot be opened."
  install -m 0600 "$(compose_dir)/.env" "$target/env" 2>/dev/null \
    || warn "Could not copy .env."

  ok "Wrote $target"
  find "$target" -maxdepth 1 -type f -exec ls -l {} + 2>/dev/null | sed 's/^/      /' || true
  printf '\n'
  warn "This is not a backup until it is off this machine and you have restored"
  warn "it once into an empty stack. Nothing else proves it works."
}

act_restore() {
  step "Restore"
  local dir
  dir="$INSTALL_DIR/backups"
  if [ ! -d "$dir" ] || [ -z "$(ls -A "$dir" 2>/dev/null || true)" ]; then
    warn "No backups in $dir."
    info "Point this at one you copied back onto this host by putting its"
    info "directory (postgres.dump plus kek.key) there first."
    return 0
  fi
  find "$dir" -mindepth 1 -maxdepth 1 -exec basename {} \; 2>/dev/null | sort | sed 's/^/      /'
  printf '\n'
  warn "Restoring replaces the current database. What is in it now is gone."
  warn "It only works with the key-encryption key the dump was taken with: a"
  warn "mismatch shows up as \"is the KEK the one used to initialize this"
  warn "database?\" and nothing but the right key fixes it."
  printf '\n'
  local which src
  which="$(ask_value "Which backup (name as listed above)?" "")"
  [ -n "$which" ] || return 0
  src="$dir/$which"
  [ -f "$src/postgres.dump" ] || { warn "$src/postgres.dump is not there."; return 0; }
  confirm_word "restore" || { info "Cancelled."; return 0; }

  if [ -f "$src/kek.key" ]; then
    if ! cmp -s "$src/kek.key" "$(compose_dir)/secrets/kek.key"; then
      warn "The key in this backup is not the key this deployment is using."
      warn "Replacing the live key is the one irreversible step in this script:"
      warn "whatever is already encrypted under the current key stays encrypted"
      warn "under it, and only the timestamped copy kept below can open it again."
      if ask_yes_no "Replace the live key with the one in this backup?" "n" \
         && confirm_word "replace-key"; then
        # Never overwritten. A fixed kek.key.previous means a second restore
        # destroys the key the first one saved, and every dump ever taken under
        # that key becomes unopenable by anyone.
        local saved
        saved="$(compose_dir)/secrets/kek.key.$(date -u '+%Y%m%dT%H%M%SZ').previous"
        cp -p "$(compose_dir)/secrets/kek.key" "$saved" \
          || { warn "Could not save a copy of the current key; leaving it in place."; return 0; }
        install -m 0644 "$src/kek.key" "$(compose_dir)/secrets/kek.key"
        ok "Installed the backup's key; the old one is $saved."
      else
        warn "Keeping the current key. The restore will very likely not open."
      fi
    fi
  else
    warn "No kek.key in this backup. If it was taken from another deployment the"
    warn "restored rows will not decrypt."
  fi

  # The replicas hold open connections to the objects `pg_restore --clean` is
  # about to drop, and a DROP on an object still in use fails. Stopping them
  # first is what makes the restore clean rather than partial; the
  # rolling_restart below brings them back.
  info "Stopping the server replicas so the restore is not fighting them."
  "$CTL" stop spinneret >/dev/null 2>&1 || true

  info "Restoring."
  if ! "$CTL" exec -T postgres pg_restore -U spinneret -d spinneret --clean --if-exists <"$src/postgres.dump"; then
    warn "pg_restore reported errors. Read the output above before trusting this."
  fi

  # Mandatory, not optional: the hot state in Valkey describes the database that
  # was there a moment ago. Until it is rebuilt every replica reports
  # "hotstate: epoch missing" and the load balancer serves nothing.
  info "Rebuilding the hot state from the restored database."
  spnr_run rebuild 2>&1 | sed 's/^/    /' || warn "The rebuild failed; run menu entry 5 before trusting the restore."

  info "Restarting."
  rolling_restart || warn "The stack did not come back healthy. Try: $CTL logs --tail 100 spinneret"
  act_diagnose
}

# Disk. Scoped to this project's own images on purpose: a plain
# `docker image prune -a` would take unrelated images off a host that runs other
# stacks, which is not this script's to decide. The build-cache option below is
# the exception - BuildKit's cache is per host and its records carry no project
# label - and it says so where it is offered.
act_free_disk() {
  step "Free disk space"
  info "Before:"
  docker system df 2>/dev/null | sed 's/^/      /' || true

  printf '\n'
  dim "The two things that grow without a ceiling here are old image tags and"
  dim "build cache. The ClickHouse volume also grows, but it expires its own"
  dim "rows on a TTL, so deleting it by hand is not the answer."
  printf '\n'

  # "In use" means referenced by a container that exists, running or stopped -
  # not "matches the tag in .env". A build-from-source install has no tag in
  # .env at all, and with that as the filter this would offer to delete the
  # image the stack is serving from.
  #
  # Both halves of the comparison happen in one awk, for two reasons. The
  # reference and the image id arrive separated by a tab because the global IFS
  # has no space in it, so a `read -r ref id` here cannot split them - `ref`
  # would carry the id too and the in-use test could never match. And the tag to
  # keep is compared as a fixed string rather than interpolated into a regex:
  # SPINNERET_IMAGE comes out of .env, and a value like `a|.*` in an ERE matches
  # every image on the host.
  #
  # The names matched are this compose project's own. The shipped file tags its
  # build `spinneret:local` whatever the project is called, so a trial run with
  # SPINNERET_PROJECT set has its own prefix (see write_build_override) and
  # cannot list the images a real stack is serving from.
  local in_use images local_repo
  if [ "$PROJECT" = "spinneret" ]; then local_repo="spinneret"; else local_repo="$PROJECT-spinneret"; fi
  in_use="$(docker ps -a --format '{{.Image}}' 2>/dev/null | sort -u || true)"
  images="$(docker images --format '{{.Repository}}:{{.Tag}}\t{{.ID}}' 2>/dev/null \
    | V_LOCAL="$local_repo" V_REPO="$IMAGE_REPO" V_USED="$in_use" awk -F'\t' '
        BEGIN {
          n = split(ENVIRON["V_USED"], u, "\n")
          for (i = 1; i <= n; i++) if (u[i] != "") used[u[i]] = 1
          local_repo = ENVIRON["V_LOCAL"]
          published  = ENVIRON["V_REPO"]
        }
        NF < 2 { next }
        {
          ref = $1
          repo = ref
          sub(/:[^:]*$/, "", repo)
          if (repo != local_repo && index(repo, local_repo "-") != 1 && repo != published) next
          if (ref in used) next
          print ref " " $2
        }' || true)"

  if [ -n "$images" ]; then
    info "Images from this project that no container references:"
    printf '%s\n' "$images" | sed 's/^/      /'
    printf '\n'
    if ask_yes_no "Remove them?" "n"; then
      printf '%s\n' "$images" | awk '{print $2}' | sort -u | while read -r id; do
        docker rmi "$id" >/dev/null 2>&1 && info "removed $id" || true
      done
    fi
  else
    ok "No stale images from this project."
  fi

  printf '\n'
  dim "Build cache is what makes a rebuild after a small change take seconds"
  dim "instead of ten minutes. BuildKit's cache is shared by every build on this"
  dim "host and its records carry no project label, so there is no way to clear"
  dim "only this project's share: this clears the host's, and the next build of"
  dim "anything on this machine is a cold one."
  if ask_yes_no "Clear this host's entire build cache?" "n"; then
    docker builder prune -af 2>&1 | tail -2 | sed 's/^/    /' || true
  fi

  printf '\n'
  info "After:"
  docker system df 2>/dev/null | sed 's/^/      /' || true
}

# ----------------------------------------------------------------- menus --

menu_uninstall() {
  while true; do
    step "Stop or remove"
    info "  1  Stop the stack, keep every byte of data"
    info "  2  Stop and delete the data volumes — PostgreSQL, Valkey, ClickHouse"
    info "  3  As 2, and remove $INSTALL_DIR, including the key-encryption key"
    info "  b  Back"
    case "$(ask_choice "Which?")" in
      1)
        "$CTL" down --remove-orphans && ok "Stopped. Start it again with: $CTL up -d --wait"
        return 0 ;;
      2)
        printf '\n'
        warn "This deletes the PostgreSQL volume — every identity, proxy, policy,"
        warn "config, secret, user and token in this deployment."
        dim  "The key-encryption key in $(compose_dir)/secrets/kek.key is kept,"
        dim  "so a dump taken with it can still be restored into a fresh stack."
        confirm_word "delete" || { info "Cancelled."; continue; }
        "$CTL" down -v --remove-orphans && ok "Stopped, and the volumes are gone."
        return 0 ;;
      3)
        printf '\n'
        warn "This deletes the volumes AND $INSTALL_DIR."
        warn "That directory holds .env and secrets/kek.key. Without that key no"
        warn "backup of this deployment can ever be opened again, by anyone."
        printf '\n'
        if [ -f "$(compose_dir)/secrets/kek.key" ] \
           && ask_yes_no "Print the key first so you can copy it somewhere?" "n"; then
          printf '\n'
          cat "$(compose_dir)/secrets/kek.key" | sed 's/^/    /'
          printf '\n'
          pause_for_reader
        fi
        confirm_word "delete" || { info "Cancelled."; continue; }
        "$CTL" down -v --remove-orphans || true
        # Guarded rather than trusting the variable: this is the one place the
        # script removes a tree, and an empty or silly value must not reach rm.
        case "$INSTALL_DIR" in
          ""|"/"|"/usr"|"/etc"|"/var"|"/home"|"/root"|"/opt"|"/bin"|"/sbin"|"/lib"|"/boot")
            die "Refusing to remove $INSTALL_DIR." ;;
        esac
        [ -f "$INSTALL_DIR/deploy/compose/docker-compose.yml" ] \
          || die "$INSTALL_DIR does not look like an install; not removing it."
        rm -rf -- "$INSTALL_DIR" && ok "Removed $INSTALL_DIR."
        info "Docker images are left alone; this script does not prune a shared host."
        return 0 ;;
      b|"") return 0 ;;
      *) warn "Pick 1, 2, 3 or b." ;;
    esac
  done
}

menu_manage() {
  while true; do
    step "Manage"
    info "  1  Change an administrator password    7  Health check"
    info "  2  Add an administrator                8  Logs"
    info "  3  List accounts                       9  Restart services"
    info "  4  Create an API token                10  Back up now"
    info "  5  Rebuild the hot state              11  Restore a backup"
    info "  6  Show the configuration             12  Free disk space"
    info "  b  Back"
    case "$(ask_choice "Which?")" in
      1)  act_change_password; pause_for_reader ;;
      2)  act_add_admin; pause_for_reader ;;
      3)  act_list_users; pause_for_reader ;;
      4)  act_create_token; pause_for_reader ;;
      5)  act_rebuild; pause_for_reader ;;
      6)  act_show_config; pause_for_reader ;;
      7)  act_diagnose; pause_for_reader ;;
      8)  act_logs; pause_for_reader ;;
      9)  act_restart; pause_for_reader ;;
      10) act_backup; pause_for_reader ;;
      11) act_restore; pause_for_reader ;;
      12) act_free_disk; pause_for_reader ;;
      b|"") return 0 ;;
      *) warn "Pick a number from the list, or b." ;;
    esac
  done
}

menu_existing() {
  local installed latest upgrade_line=""
  installed="$(running_version || true)"
  latest="$(latest_release)"
  # Straight off the network, so checked before it is shown or offered.
  case "$latest" in ''|[!A-Za-z0-9_]*|*[!A-Za-z0-9._-]*) latest="" ;; esac

  step "An install is already here"
  ok "$INSTALL_DIR"
  if [ -n "$installed" ]; then
    ok "Running $installed"
  else
    warn "Not running, or no replica is answering yet."
  fi
  if [ "$USE_PUBLISHED" = 1 ]; then
    ok "Image $IMAGE_REPO:$IMAGE_TAG"
  else
    ok "Built from source"
  fi
  if [ -n "$latest" ] && [ -n "$installed" ] && version_is_newer "$latest" "$installed"; then
    upgrade_line="$latest"
    warn "$latest is available."
  fi

  while true; do
    printf '\n'
    info "  1  Status — versions, containers, schema, disk"
    if [ -n "$upgrade_line" ]; then
      info "  2  Upgrade to $upgrade_line"
    elif [ "$USE_PUBLISHED" = 1 ]; then
      info "  2  Move to another image tag (re-pull, migrate, restart)"
    else
      info "  2  Rebuild from the latest source (fetch, build, migrate, restart)"
    fi
    info "  3  Manage — accounts, tokens, backups, health, disk"
    info "  4  Stop or remove this install"
    info "  q  Quit"
    case "$(ask_choice "Which?")" in
      1) show_status; pause_for_reader ;;
      2)
        if [ "$USE_PUBLISHED" != 1 ]; then
          # There is no tag to choose: the image is whatever the checkout builds.
          do_upgrade "source"
          pause_for_reader
          continue
        fi
        local target
        if [ -n "$upgrade_line" ]; then
          target="$(ask_value "Which tag?" "$upgrade_line" valid_image_tag)"
        else
          # No newer release does not mean nowhere to go. The same field takes
          # an older tag, which is how a rollback is done.
          dim "No newer release. You can still move to another published tag,"
          dim "including an older one to roll back to."
          target="$(ask_value "Which tag?" "$IMAGE_TAG" valid_image_tag)"
        fi
        if [ -n "$target" ]; then
          [ "$target" = "$IMAGE_TAG" ] && info "Already on $target. Re-pulling and restarting anyway."
          do_upgrade "$target"
        fi
        pause_for_reader ;;
      3) menu_manage ;;
      4) menu_uninstall; return 0 ;;
      q|"") return 0 ;;
      *) warn "Pick 1, 2, 3, 4 or q." ;;
    esac
  done
}

# -------------------------------------------------------------------- main --

usage() {
  cat <<EOF
Usage: bash install.sh [options]

  --yes, -y      Take the default answer to every question. Publishes on
                 127.0.0.1:8080, one administrator called admin, replicas
                 scaled to this host, observability off.
  --check        Detect the system and print what would happen, then stop.
                 Changes nothing, writes nothing, asks nothing.
  --manage       Go straight to the menu for an install that already exists.
  --help, -h     This text.

Run it with no options and it works out which job it is doing: it installs when
there is nothing here, and opens a menu when there is — status, upgrade,
accounts, tokens, backups, health, disk, uninstall.

Environment (each one presets an answer, so --yes becomes unattended):
  SPINNERET_PROJECT          Compose project name. Default: spinneret.
  SPINNERET_INSTALL_DIR      Where to install. Default: /opt/spinneret as root,
                             ~/spinneret otherwise.
  SPINNERET_BIND_HOST        127.0.0.1 (default) or 0.0.0.0.
  SPINNERET_PORT             Published port. Default: 8080.
  SPINNERET_ADMIN_USERNAME   First administrator. Default: admin.
  SPINNERET_REPLICAS         Server replicas. Default: derived from CPU and RAM.
  SPINNERET_ENABLE_OBSERVABILITY  1 to add Prometheus, 0 (default) not to.
  SPINNERET_USE_PUBLISHED    1 (default) to pull the image, 0 to build from source.
  SPINNERET_IMAGE            Image repository. Default: $DEFAULT_IMAGE.
  SPINNERET_IMAGE_TAG        Image tag. Default: latest.
  NO_COLOR                   Set to anything to turn off colour.

The administrator password and the database passwords are always generated on
this machine and never taken from the environment.

Documentation: $DOCS_EN
               $DOCS_ZH
EOF
}

main() {
  while [ $# -gt 0 ]; do
    case "$1" in
      -y|--yes)  ASSUME_YES=1 ;;
      --check)   CHECK_ONLY=1 ;;
      --manage)  MANAGE_ONLY=1 ;;
      -h|--help) usage; exit 0 ;;
      *)         die "Unknown option: $1  (try --help)" ;;
    esac
    shift
  done

  banner
  detect_os
  detect_resources
  report_host

  step "Checking Docker"
  if ! check_docker_present; then
    if [ "$CHECK_ONLY" = 1 ]; then
      warn "Docker is not installed. The script would offer to install it."
      info "Package manager command for this system:"
      dim  "  $(docker_install_hint)"
      exit 0
    fi
    offer_docker_install
  fi
  ok "Docker $(docker --version 2>/dev/null | sed 's/Docker version //; s/,.*//')"

  if [ "$CHECK_ONLY" = 1 ]; then
    check_compose || true
    if check_docker_running; then ok "The Docker daemon is answering."; else warn "The Docker daemon is not answering."; fi
    for tool in git curl; do
      if have "$tool"; then ok "$tool"; else warn "$tool is missing: $(pkg_install_hint "$tool")"; fi
    done
    if image_available "$IMAGE_REPO:$IMAGE_TAG"; then
      ok "$IMAGE_REPO:$IMAGE_TAG can be pulled."
    else
      warn "$IMAGE_REPO:$IMAGE_TAG cannot be fetched; the install would build from source."
    fi
    step "Check only"
    info "Nothing was changed. Run without --check to install."
    exit 0
  fi

  ensure_docker_running
  check_compose || die "Compose v2 $COMPOSE_MIN or newer is required."
  have curl || die "curl is not installed. Install it with $(pkg_install_hint curl), then run this again."

  # An install that is already here turns this into a management tool rather
  # than an installer. Asked of Docker, so it is found wherever it was put.
  #
  # Looked for unconditionally. Gated on --manage, or on neither --yes nor
  # SPINNERET_INSTALL_DIR being set, a run with either of those falls through to
  # the install path and reconfigures a deployment that is already here from this
  # script's own defaults: a build-from-source install switched to the published
  # `latest`, that image's migrations applied to the production database, the
  # observability profile silently dropped, and compose.host.yml rewritten over
  # whatever the operator had edited into it.
  local found=""
  if [ -n "${SPINNERET_INSTALL_DIR:-}" ] && [ -x "${SPINNERET_INSTALL_DIR}/spnrctl" ]; then
    found="$SPINNERET_INSTALL_DIR"
  else
    found="$(find_existing_install || true)"
  fi
  if [ "$MANAGE_ONLY" = 1 ] && { [ -z "$found" ] || [ ! -x "$found/spnrctl" ]; }; then
    die "No install found for project '$PROJECT'.
    Run this script without --manage to create one, or set
    SPINNERET_INSTALL_DIR to where it lives."
  fi

  if [ -n "$found" ] && [ -x "$found/spnrctl" ]; then
    INSTALL_DIR="$found"
    CTL="$found/spnrctl"
    load_install_settings
    if [ "$MANAGE_ONLY" = 0 ] && [ "$ASSUME_YES" = 1 ]; then
      # --yes means "take the default answer to every question", and over a
      # deployment that already exists the default answer to "change the image
      # source, the profile set and the published port" is no. So: nothing.
      step "An install is already here"
      ok "$INSTALL_DIR"
      info "Nothing was changed. --yes answers questions from defaults, and over a"
      info "live deployment those are not its settings. To work on this one:"
      info "  bash install.sh --manage     or:  $CTL ps"
      exit 0
    fi
    if [ -z "$TTY_IN" ]; then
      warn "An install is already at $INSTALL_DIR."
      info "Run this from a terminal to manage it, or use $CTL directly."
      exit 0
    fi
    menu_existing
    exit 0
  fi

  if [ -z "$TTY_IN" ] && [ "$ASSUME_YES" = 0 ]; then
    die "No terminal to ask questions on.
    Either download and run the script:
      curl -fsSL $REPO_RAW/install/install.sh -o install.sh && bash install.sh
    or accept every default:
      curl -fsSL $REPO_RAW/install/install.sh | bash -s -- --yes"
  fi

  choose_directory
  prepare_directory
  ask_questions
  write_env
  write_kek
  write_image_override
  write_host_override
  write_control_script
  CTL="$INSTALL_DIR/spnrctl"
  bring_up
  wait_ready
  bootstrap_admin
  show_result
}

main "$@"
