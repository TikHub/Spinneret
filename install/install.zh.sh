#!/usr/bin/env bash
#
# Spinneret — guided Docker install (Simplified Chinese).
#
#   curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh -o install.zh.sh
#   less install.zh.sh       # read it first; you are about to run it
#   bash install.zh.sh
#
# Docker only. Installing without Docker means putting PostgreSQL, Valkey,
# ClickHouse, Go and Node on the host yourself and wiring them together, which
# is a document rather than a script: documents/zh/02-installation.md.
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
# The English version of this script is install.sh in the same directory.
# The two are kept structurally identical; only the messages differ.

set -euo pipefail
IFS=$'\n\t'

# ---------------------------------------------------------------- constants --

readonly REPO_URL="https://github.com/TikHub/Spinneret.git"
readonly REPO_RAW="https://raw.githubusercontent.com/TikHub/Spinneret/main"
readonly REPO_API="https://api.github.com/repos/TikHub/Spinneret"
readonly DOCKER_INSTALL_URL="https://get.docker.com"
readonly DOCS_INSTALL="https://github.com/TikHub/Spinneret/blob/main/documents/zh/02-installation.md"
readonly DOCS_EN="https://github.com/TikHub/Spinneret/blob/main/documents/en/01-quickstart.md"
readonly DOCS_ZH="https://github.com/TikHub/Spinneret/blob/main/documents/zh/01-quickstart.md"

# Compose 2.24 is the first release with the `!reset` and `!override` merge tags,
# and this script writes override files that use both. Anything older fails in
# ways that read like a problem with this project.
readonly COMPOSE_MIN="2.24"

# The published multi-arch image. Docker Hub rather than GitHub Packages because
# this is the copy anyone can pull without credentials; the same build is pushed
# to ghcr.io/tikhub/spinneret at the same digest, and that one needs a login.
# SPINNERET_IMAGE replaces the repository part so a private mirror, a fork or the
# GitHub Packages copy can be used without editing this script.
readonly DEFAULT_IMAGE="tikhubio/spinneret"

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
    printf '错误：SPINNERET_PROJECT 只能包含字母、数字、短横线或下划线。\n' >&2
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
die()  { printf '\n%s错误：%s%s\n\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }

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
  # The hint advertises the Chinese answers the case statement below accepts;
  # a feature nothing in the interface mentions is not a feature.
  if [ "$default" = "y" ]; then hint="[Y/n，也可答 是/否]"; else hint="[y/N，也可答 是/否]"; fi
  if [ "$ASSUME_YES" = 1 ] || [ -z "$TTY_IN" ]; then
    [ "$default" = "y" ]
    return
  fi
  while true; do
    printf '    %s %s ' "$prompt" "$hint" >&2
    reply="$(read_line)"
    reply="$(printf '%s' "$reply" | LC_ALL=C tr '[:upper:]' '[:lower:]' | LC_ALL=C tr -d '[:space:]')"
    case "$reply" in
      "")            [ "$default" = "y" ]; return ;;
      y|yes|是|是的) return 0 ;;
      n|no|否|不)    return 1 ;;
      *)             warn "请回答 y 或 是，或者 n 或 否。" ;;
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
    ''|*[!0-9]*) warn "端口必须是数字。"; return 1 ;;
  esac
  if [ "$1" -lt 1 ] || [ "$1" -gt 65535 ]; then
    warn "端口范围是 1 到 65535。"
    return 1
  fi
  return 0
}

valid_path() {
  case "$1" in
    /|"") warn "这个目录不适合安装到里面。"; return 1 ;;
    # ~ and ~/… only: choose_directory expands exactly those two forms, and a
    # ~user path accepted here would become a literal directory called ~user
    # under the current working directory.
    /*|~|~/*|./*|../*) return 0 ;;
    *) warn "请用绝对路径，例如 /opt/spinneret。"; return 1 ;;
  esac
}

# The server enforces ^[a-z0-9][a-z0-9._-]{2,63}$ on a username
# (internal/auth/validate.go). Rejecting it here saves a failed bootstrap.
valid_username() {
  case "$1" in
    ''|[!a-z0-9]*) warn "必须以小写字母或数字开头。"; return 1 ;;
    *[!a-z0-9._-]*) warn "只能用小写字母、数字、点、下划线和短横线。"; return 1 ;;
  esac
  if [ "${#1}" -lt 3 ] || [ "${#1}" -gt 64 ]; then
    warn "长度在 3 到 64 个字符之间。"
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
    ''|[!A-Za-z0-9_]*) warn "tag 要以字母、数字或下划线开头。"; return 1 ;;
    *[!A-Za-z0-9._-]*) warn "只能用字母、数字、点、下划线和短横线。"; return 1 ;;
  esac
  if [ "${#1}" -gt 128 ]; then
    warn "这比镜像仓库允许的 tag 长度还长。"
    return 1
  fi
  return 0
}

valid_replicas() {
  case "$1" in
    ''|*[!0-9]*) warn "这是个数量，请填数字。"; return 1 ;;
  esac
  if [ "$1" -lt 1 ] || [ "$1" -gt 64 ]; then
    warn "范围是 1 到 64。"
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
      warn "当前 Compose 是 ${raw}，这套编排需要 $COMPOSE_MIN 或更高。"
      info "升级 Docker 后再跑一次。参见 $DOCS_INSTALL"
      return 1
    fi
    ok "Docker Compose $raw"
    return 0
  fi
  if have docker-compose; then
    warn "检测到旧的独立版 docker-compose（v1）。"
    info "这套编排用到了 Compose v2 的合并标签。请安装较新的 Docker，它自带"
    info "Compose 插件：$DOCKER_INSTALL_URL"
    return 1
  fi
  warn "没有安装 Docker Compose v2。"
  return 1
}

docker_install_hint() {
  case "$(distro_family)" in
    debian) printf '%s' "sudo apt-get update && sudo apt-get install -y docker.io docker-compose-v2" ;;
    rhel)   printf '%s' "sudo dnf install -y docker docker-compose-plugin  # 或者用 yum" ;;
    suse)   printf '%s' "sudo zypper install -y docker docker-compose" ;;
    arch)   printf '%s' "sudo pacman -S --needed docker docker-compose" ;;
    alpine) printf '%s' "sudo apk add docker docker-cli-compose && sudo rc-update add docker default" ;;
    nixos)  printf '%s' "在 configuration.nix 里加一行 virtualisation.docker.enable = true;" ;;
    void)   printf '%s' "sudo xbps-install -S docker docker-compose" ;;
    gentoo) printf '%s' "sudo emerge app-containers/docker app-containers/docker-compose" ;;
    *)      printf '%s' "参见 https://docs.docker.com/engine/install/" ;;
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
    *)      printf '%s' "用你的包管理器安装 $pkg" ;;
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
      die "没有安装 Docker。
    在 macOS 上请安装 Docker Desktop 或 OrbStack，启动它，然后再跑一次本脚本：
      https://www.docker.com/products/docker-desktop/
    Homebrew：brew install --cask docker" ;;
    wsl)
      die "这个 WSL 发行版里没有可用的 Docker。
    请在 Windows 上安装 Docker Desktop，并为这个发行版打开 WSL 集成，然后再跑
    一次本脚本：
      https://docs.docker.com/desktop/wsl/" ;;
    unsupported)
      die "本脚本只在 Linux 上安装 Docker，在 macOS 上要求你自己装 Docker Desktop。
    在 $DISTRO_NAME 上请自行安装 Docker，然后再跑一次本脚本：
      https://docs.docker.com/engine/install/" ;;
  esac

  warn "没有安装 Docker。"
  info ""
  info "有两条路，都需要 sudo。"
  info ""
  info "  1. 用你的包管理器："
  dim  "     $(docker_install_hint)"
  if convenience_script_supported; then
    info ""
    info "  2. Docker 官方安装脚本，本脚本可以替你执行："
    dim  "     curl -fsSL $DOCKER_INSTALL_URL | sudo sh"
    info ""
    info "     这会从 Docker Inc 下载一个 shell 脚本并以 root 身份运行。"
    info "     它是官方脚本，也是 Docker 自己的文档推荐的做法，"
    info "     但毕竟是以 root 身份跑远端代码，所以这一步交给你决定，"
    info "     默认不做。"
    info ""
    if ask_yes_no "现在就运行 Docker 官方安装脚本吗？" "n"; then
      step "正在安装 Docker"
      local tmp
      tmp="$(mktemp -t spinneret-docker-install.XXXXXX)" || die "无法创建临时文件。"
      TMP_FILES+=("$tmp")
      # Download first, then run: a pipe straight into a shell cannot be
      # inspected, and a truncated download becomes a half-executed script.
      if ! curl -fsSL --proto '=https' --tlsv1.2 "$DOCKER_INSTALL_URL" -o "$tmp"; then
        rm -f "$tmp"
        die "下载 Docker 安装脚本失败。请手动安装 Docker，然后再跑一次本脚本。"
      fi
      if [ ! -s "$tmp" ]; then
        rm -f "$tmp"
        die "下载到的 Docker 安装脚本是空的。请手动安装 Docker，然后再跑一次本脚本。"
      fi
      info "已下载到 ${tmp}（$(wc -c <"$tmp" | tr -d ' ') 字节）。"
      as_root sh "$tmp" || { rm -f "$tmp"; die "Docker 安装脚本执行失败。请看上面的输出。"; }
      rm -f "$tmp"
      ok "Docker 安装完成。"
    else
      die "用上面那条命令装好 Docker，然后再跑一次本脚本。"
    fi
  else
    info ""
    info "  2. Docker 官方安装脚本不支持 ${DISTRO_NAME}，所以请用上面的"
    info "     包管理器命令。"
    die "装好 Docker，然后再跑一次本脚本。"
  fi
}

ensure_docker_running() {
  if check_docker_running; then
    return 0
  fi

  if [ "$OS" = "macos" ]; then
    die "Docker 已安装但没在运行。启动 Docker Desktop（或 OrbStack），等它显示已
    就绪，然后再跑一次本脚本。"
  fi

  warn "Docker 已安装，但没有响应。"
  if have systemctl && ask_yes_no "现在启动 Docker 服务吗？" "y"; then
    as_root systemctl enable --now docker || true
    sleep 2
  elif have rc-service && ask_yes_no "现在启动 Docker 服务吗？" "y"; then
    as_root rc-update add docker default || true
    as_root rc-service docker start || true
    sleep 2
  fi

  if check_docker_running; then
    ok "Docker 正在运行。"
    return 0
  fi

  # The other common cause: the daemon is up but this user is not allowed to
  # talk to it. Say which of the two it is rather than "cannot connect".
  if [ "$(id -u)" != "0" ] && sudo -n docker info >/dev/null 2>&1; then
    die "Docker 在运行，但当前用户没有权限访问它。
    把自己加进 docker 组，然后重新登录一个 shell：
      sudo usermod -aG docker \"\$USER\"
      newgrp docker
    然后再跑一次本脚本。（在这台机器上，docker 组权限等同于 root，所以本脚本
    不会替你做这件事。）"
  fi

  die "Docker 已安装但没在运行，尝试启动也没成功。
    请按这个系统的方式把它启动起来，用 'docker info' 确认，然后再跑一次本脚本。"
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
  case "$want" in ''|*[!0-9]*) die "random_alnum：长度参数不合法。" ;; esac
  while [ "${#out}" -lt "$want" ]; do
    if [ -r /dev/urandom ]; then
      chunk="$(LC_ALL=C head -c 512 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9' || true)"
    elif have openssl; then
      chunk="$(openssl rand -base64 384 | LC_ALL=C tr -dc 'A-Za-z0-9' || true)"
    else
      die "找不到随机源（/dev/urandom 或 openssl），无法生成密钥口令。"
    fi
    [ -n "$chunk" ] || die "读取随机数失败，无法生成密钥口令。"
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
    die "找不到随机源（/dev/urandom 或 openssl），无法生成 KEK。"
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
  printf '%s  Spinneret · 引导式部署%s\n' "$C_BOLD" "$C_RESET"
  printf '%s  只走 Docker。没问过你之前，不装任何东西、不改任何东西。%s\n' "$C_DIM" "$C_RESET"
  printf '\n'
}

report_host() {
  step "看一眼这台机器"
  ok "${DISTRO_NAME}（${ARCH}）"
  if [ "$MEM_MIB" -gt 0 ]; then
    ok "$CPUS 核 CPU，$(( MEM_MIB / 1024 )).$(( (MEM_MIB % 1024) * 10 / 1024 )) GiB 内存"
  else
    ok "$CPUS 核 CPU，内存大小未知"
  fi

  case "$ARCH" in
    x86_64|arm64) : ;;
    *) die "官方镜像只发布 x86_64 和 arm64，这台机器是 ${ARCH}。
    你仍然可以在这台机器上从源码构建：$DOCS_INSTALL" ;;
  esac

  # Numbers from the stack's own measurements. ClickHouse idles at about 1.2 GiB
  # whatever its caches are set to, and PostgreSQL allocates 512 MiB of shared
  # buffers at start, so 4 GiB is the floor for the stack as shipped.
  if [ "$MEM_MIB" -gt 0 ] && [ "$MEM_MIB" -lt 3800 ]; then
    warn "内存不足 4 GiB。默认编排需要 4 GiB：单是 ClickHouse 空跑就占约 1.2 GiB，"
    warn "PostgreSQL 还要 512 MiB 共享缓冲区。"
    warn "它仍然能起来，但机器一忙就会撞上 OOM killer。"
  fi
  if [ "$CPUS" -lt 2 ]; then
    warn "只有 1 核 CPU。首次启动会很慢；2 核才是实际底线。"
  fi
}

choose_directory() {
  step "装到哪里"
  local default_dir
  if [ -n "${SPINNERET_INSTALL_DIR:-}" ]; then
    default_dir="$SPINNERET_INSTALL_DIR"
  elif [ "$(id -u)" = "0" ]; then
    default_dir="/opt/spinneret"
  else
    default_dir="$HOME/spinneret"
  fi
  dim "代码检出、你的 .env、密钥加密密钥和控制脚本都放在这里。"
  dim "数据库在 Docker 数据卷里，不在这个目录下。"
  INSTALL_DIR="$(ask_value "安装目录？" "$default_dir" valid_path)"

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
      die "拒绝安装到 ${INSTALL_DIR}。" ;;
  esac
}

prepare_directory() {
  if [ -e "$INSTALL_DIR" ] && [ ! -d "$INSTALL_DIR" ]; then
    die "$INSTALL_DIR 已存在，而且不是目录。"
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
      die "$INSTALL_DIR 是另一个仓库的检出（${origin}）。
    Spinneret 的检出里会有 deploy/compose/docker-compose.yml。
    请换一个空目录。"
    fi
    ok "在 $INSTALL_DIR 找到了已有的检出。"
    info "你的 .env、密钥加密密钥和 Docker 数据卷都不会被动。"
    if ask_yes_no "把检出更新到最新的 main 吗？" "y"; then
      git -C "$INSTALL_DIR" fetch --quiet origin main || warn "拉取失败，就用当前检出继续。"
      git -C "$INSTALL_DIR" checkout --quiet main 2>/dev/null || true
      # --ff-only, never --hard: a reset would throw away edits somebody
      # made on purpose, and this script has no business doing that.
      git -C "$INSTALL_DIR" merge --ff-only --quiet origin/main \
        || warn "检出里有本地改动，保持原样不动。"
    fi
    return
  fi

  if [ -d "$INSTALL_DIR" ] && [ -n "$(ls -A "$INSTALL_DIR" 2>/dev/null || true)" ]; then
    die "$INSTALL_DIR 不是空的，也不是已有的安装。请换一个目录。"
  fi

  step "获取源码"
  have git || die "没有安装 git。用 $(pkg_install_hint git) 装上，然后再跑一次本脚本。"
  dim "完整克隆仓库，因为回退路径要用它来构建镜像，"
  dim "而 compose 的构建上下文就是仓库根目录。"

  local parent
  parent="$(dirname "$INSTALL_DIR")"
  if [ ! -d "$parent" ]; then
    # Named before it is created: this is the only thing the script makes outside
    # the directory the operator chose, and `mkdir -p` makes every level of it.
    info "$parent 不存在；会把它以及它上面缺失的每一级目录一并创建。"
    as_root mkdir -p "$parent"
  fi
  if [ ! -w "$parent" ]; then
    as_root mkdir -p "$INSTALL_DIR"
    as_root chown "$(id -u):$(id -g)" "$INSTALL_DIR"
  fi

  git clone --quiet --depth 1 --branch main "$REPO_URL" "$INSTALL_DIR" \
    || die "克隆 $REPO_URL 失败。检查网络后重试：$DOCS_INSTALL"
  ok "已克隆到 $INSTALL_DIR"
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
  step "几个问题"

  dim "127.0.0.1 表示只有这台机器能访问控制台。控制台是管理面，而 /metrics 和它"
  dim "共用同一个监听端口且没有鉴权，所以只有在带 TLS 的反向代理后面，才应该把它"
  dim "发布到所有网卡上。"
  local loopback_default="y"
  [ "$BIND_HOST" = "0.0.0.0" ] && loopback_default="n"
  if ask_yes_no "保持只监听 127.0.0.1（推荐）吗？" "$loopback_default"; then
    BIND_HOST="127.0.0.1"
  else
    BIND_HOST="0.0.0.0"
    warn "将发布在 0.0.0.0 上。在别人能路由到这台机器之前，先在前面架好 TLS。"
    warn "然后在 .env 里设 SPINNERET_COOKIE_SECURE=true，别让会话 Cookie 明文传输。"
  fi
  BIND_PORT="$(ask_value "用哪个端口？" "$BIND_PORT" valid_port)"

  printf '\n'
  dim "控制台的第一个管理员。口令在本机生成，最后只显示一次；"
  dim "不会发往任何地方。"
  ADMIN_USER="$(ask_value "管理员用户名？" "$ADMIN_USER" valid_username)"

  printf '\n'
  dim "负载均衡后面的服务端副本数。副本是无状态的。1 个副本在升级时会有"
  dim "一段空窗，2 个副本就没有，代价是多占大约 150-500 MiB 内存。"
  local repl_default="$REPLICAS"
  if [ -z "$repl_default" ]; then
    # One replica per two cores, clamped: below 4 GiB a second replica competes
    # with PostgreSQL and ClickHouse for memory that is not there.
    repl_default=$(( CPUS / 2 ))
    [ "$repl_default" -lt 1 ] && repl_default=1
    [ "$repl_default" -gt 4 ] && repl_default=4
    if [ "$MEM_MIB" -gt 0 ] && [ "$MEM_MIB" -lt 3800 ]; then repl_default=1; fi
  fi
  REPLICAS="$(ask_value "要几个服务端副本？" "$repl_default" valid_replicas)"
  if [ "$REPLICAS" = 1 ]; then
    dim "1 个副本：升级期间会有一小段时间没有任何实例在提供服务。"
  fi

  printf '\n'
  dim "可观测性 profile 会加一个 Prometheus，去抓服务端的 /metrics。"
  dim "它永远只发布在 127.0.0.1 上：因为它没有任何鉴权。"
  local obs_default="n"; [ "$WANT_OBSERVABILITY" = 1 ] && obs_default="y"
  if ask_yes_no "启用可观测性 profile 吗？" "$obs_default"; then
    WANT_OBSERVABILITY=1
  else
    WANT_OBSERVABILITY=0
  fi

  printf '\n'
  local ref="$IMAGE_REPO:$IMAGE_TAG"
  if [ "$USE_PUBLISHED" = 1 ] && image_available "$ref"; then
    dim "$ref 可以拉取，大约一分钟。"
    dim "改成从源码构建的话，首次构建要 5-15 分钟，"
    dim "外加大约 2 GB 构建缓存。"
    if ask_yes_no "使用已发布的镜像（推荐）吗？" "y"; then
      USE_PUBLISHED=1
    else
      USE_PUBLISHED=0
    fi
  else
    if [ "$USE_PUBLISHED" = 1 ]; then
      warn "从这里拉不到 ${ref}。"
      info "要么这个 tag 还没发布，要么这台机器连不上镜像仓库。"
      info "你的检出没有问题。"
      info ""
    fi
    dim "退回到用 $INSTALL_DIR 里的源码构建镜像。"
    dim "首次构建请预留 5-15 分钟和大约 2 GB 构建缓存；"
    dim "构建过程需要能访问 Go 和 Node 的包仓库。"
    USE_PUBLISHED=0
    if [ -n "${SPINNERET_IMAGE:-}" ] || [ -n "${SPINNERET_IMAGE_TAG:-}" ]; then
      ask_yes_no "改成从源码构建吗？" "y" || die "没有可以安装的来源。
    请先发布或镜像一份 ${ref}，或者重跑本脚本并允许它从源码构建。"
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

  step "写入环境文件"

  if [ -f "$env_file" ]; then
    ok "保留已有的 ${env_file}。"
    info "里面的口令就是当初创建数据库数据卷时用的那一套；换一份新文件就打不开"
    info "那些卷了。如果你真想从头开始，请自己删掉它。"
    ADMIN_PASSWORD="$(env_get SPINNERET_ADMIN_PASSWORD)"
    return
  fi
  [ -f "$example" ] || die "$example 不存在。$INSTALL_DIR 里的检出不完整。"

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
    || { umask "$old_umask"; die "无法在 $env_file 旁边创建临时文件。"; }
  TMP_FILES+=("$tmp")
  {
    printf '# 由 install/install.zh.sh 于 %s 生成。\n' "$(date -u '+%Y-%m-%d %H:%M:%SZ')"
    printf '# 这个文件里有数据库口令和第一个管理员的口令。\n'
    printf '# 请和 secrets/kek.key 一起备份 —— 后者是保险箱里所有数据的加密密钥。\n'
    printf '# 这两个文件丢了都没有任何恢复手段。\n'
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
    printf '# --- 以下由 install/install.zh.sh 追加 ------------------------------------------------------\n'
    printf '# 负载均衡对外监听的地址。由 compose.host.yml 读取，服务端本身不读它。\n'
    printf 'SPINNERET_BIND_HOST=%s\n' "$BIND_HOST"
    if [ "$USE_PUBLISHED" = 1 ]; then
      printf '# 已发布的镜像。这里写死一个 tag 而不是跟随滚动 tag，这样运维能说清当前跑的是哪个\n'
      printf '# 构建，也能把它换回去。由 compose.image.yml 读取。\n'
      printf 'SPINNERET_IMAGE=%s\n' "$IMAGE_REPO"
      printf 'SPINNERET_IMAGE_TAG=%s\n' "$IMAGE_TAG"
    else
      printf '# 这次安装从检出的源码构建镜像，所以没有 compose.image.yml。\n'
    fi
  } >"$tmp"
  umask "$old_umask"
  chmod 600 "$tmp"
  mv -f "$tmp" "$env_file"
  ok "已写入 ${env_file}（仅文件属主可读）。"
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
    ok "保留已有的 ${kek}。"
    chmod 0644 "$kek" 2>/dev/null || true
    return
  fi

  # Said before the key exists, not only after it does. Under `curl | bash` the
  # comment at the top of this file is not something anybody reads, and this is
  # the one decision in the install that cannot be undone afterwards.
  printf '\n'
  warn "马上要为这套部署生成密钥加密密钥。"
  warn "保险箱里的一切都是用它加密的，而且没有退路：没有这个文件，"
  warn "恢复出来的数据库谁都打不开。"
  printf '\n'

  # Generated into a variable and checked before it reaches the file: `die`
  # inside a command substitution exits only that subshell, so an unchecked
  # $(random_b64 32) can leave the literal string "k1:" behind - a KEK with no
  # key material in it, which the guard above would then keep forever.
  local key
  key="$(random_b64 32)" || key=""
  case "$key" in ''|*[!A-Za-z0-9+/=]*) key="" ;; esac
  [ "${#key}" -ge 43 ] || die "无法生成 32 字节密钥。请装上 openssl，或让 /dev/urandom
    可读，然后再跑一次。"

  old_umask="$(umask)"
  umask 077
  printf 'k1:%s\n' "$key" >"$kek"
  umask "$old_umask"
  chmod 0644 "$kek"

  ok "已写入 $kek"
  printf '\n'
  warn "这个文件是整个密钥保险箱的根。"
  warn "每一份身份凭据、代理 URL 和保险箱密钥都是用它加密的。"
  warn "没有它，恢复出来的数据库谁都打不开 —— 包括你自己。"
  warn "在往这套部署里放任何东西之前，先把它复制到安全的地方："
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
  ok "已写入 compose.build.yml —— 这个项目构建出 $PROJECT-spinneret:local。"
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
    printf '# 由 install/install.zh.sh 生成。删掉这个文件就会改成从源码构建。\n'
    printf '#\n'
    printf '# "build: !reset null" 这一行会去掉 build 段，这样 compose build 就不会悄悄在拉下来的\n'
    printf '# 镜像上重新构建一遍，compose 也不需要仓库根目录存在才能解析构建上下文。\n'
    printf '# 它要求 Compose 2.24 或更高版本。\n'
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
  ok "已写入 compose.image.yml —— 固定为 $IMAGE_REPO:${IMAGE_TAG}。"
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
    printf '# 由 install/install.zh.sh 按这台机器生成：%s 核 CPU，%s MiB 内存。\n' "$CPUS" "$MEM_MIB"
    printf '#\n'
    printf '# "ports: !override" 标签是替换端口发布列表，而不是往后追加 —— 普通合并会让同一个\n'
    printf '# 容器端口出现两条映射，而第二条一定会绑定失败。\n'
    printf '# 这个标签要求 Compose 2.24 或更高版本。\n'
    printf '#\n'
    printf '# 这个文件可以随意改。删掉它就回到默认配置。\n'
    printf 'services:\n'
    printf '  lb:\n'
    printf '    ports: !override\n'
    # Single quotes on purpose: compose expands these, not this script.
    # shellcheck disable=SC2016
    printf '      - "${SPINNERET_BIND_HOST:-127.0.0.1}:${SPINNERET_PORT:-8080}:8080"\n'
    if [ "$WANT_OBSERVABILITY" = 1 ]; then
      printf '  prometheus:\n'
      printf '    # 不管控制台绑在哪里，Prometheus 一律只绑回环：它没有任何鉴权，而它保存的\n'
      printf '    # 指标序列描述了这套部署里每一个命名空间。\n'
      printf '    ports: !override\n'
      # Single quotes on purpose: compose expands this, not this script.
      # shellcheck disable=SC2016
      printf '      - "127.0.0.1:${PROMETHEUS_PORT:-9090}:9090"\n'
    fi
    if [ "$need_mem" = 1 ]; then
      printf '  # 这些是上限，不是预留。在这个规格的机器上，默认组件加起来要的内存比实际\n'
      printf '  # 有的还多，运气不好的时候 OOM killer 挑中的会是 PostgreSQL，\n'
      printf '  # 而不是真正在涨的那个。\n'
      printf '  clickhouse:\n    mem_limit: %sm\n' "$(( MEM_MIB * 2 / 5 ))"
      printf '  postgres:\n    mem_limit: %sm\n' "$(( MEM_MIB / 4 ))"
      printf '  valkey:\n    mem_limit: %sm\n' "$(( MEM_MIB / 5 ))"
      printf '  spinneret:\n    mem_limit: %sm\n' "$(( MEM_MIB / 8 ))"
    fi
  } >"$file"

  if [ "$need_mem" = 1 ]; then
    ok "已写入 compose.host.yml —— 监听地址，以及按 $MEM_MIB MiB 换算的内存上限。"
  else
    ok "已写入 compose.host.yml —— 发布在 $BIND_HOST:${BIND_PORT}。"
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
    printf '# 这套部署的唯一入口。由 install/install.zh.sh 生成。\n'
    printf '#\n'
    printf '#   ./spnrctl ps                       看什么在跑\n'
    printf '#   ./spnrctl logs -f spinneret        跟随服务端日志\n'
    printf '#   ./spnrctl restart lb               重启某一个服务\n'
    printf '#   ./spnrctl up -d --wait             启动并等到健康\n'
    printf '#   ./spnrctl down                     停掉，数据保留\n'
    printf '#   ./spnrctl down -v                  停掉并删除数据卷。不可逆。\n'
    printf '#   ./spnrctl exec spinneret sh        进副本里开个 shell（进不去：distroless 镜像）\n'
    printf '#\n'
    printf '# 名字后面的所有参数都原样传给 docker compose，所以 compose 能做的它都能做 ——\n'
    printf '# 而且这套部署已经替你选好了。\n'
    printf '#\n'
    printf '# 管理 CLI 在镜像里，路径是 /usr/local/bin/spnr，它只依赖 PostgreSQL，所以请通过\n'
    printf '# 一次性的 migrate 服务来跑它，而不是通过某个可能还不健康的\n'
    printf '# 副本：\n'
    printf '#\n'
    printf '#   ./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status\n'
    printf '#   ./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate token create --help\n'
    printf '#\n'
    printf '# 重新打开安装脚本的菜单 —— 状态、升级、账号、令牌、备份、卸载：\n'
    printf '#\n'
    printf '#   bash install.zh.sh --manage\n'
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
  ok "已写入 spnrctl —— 以后用它，不要直接敲 docker compose。"
}

bring_up() {
  step "启动这套编排"
  local ctl="$INSTALL_DIR/spnrctl"

  if [ "$USE_PUBLISHED" = 1 ]; then
    info "正在拉取镜像。第一次会花几分钟。"
    # Deliberately not `a || b | tail || die`: a pipeline takes the exit status
    # of its LAST command, so `tail` succeeding would swallow a failed pull and
    # the install would carry on with no images.
    if ! "$ctl" pull; then
      die "拉取镜像失败，什么都还没启动。
    确认 $IMAGE_REPO:$IMAGE_TAG 存在、并且这台机器能访问镜像仓库，
    或者重跑本脚本改成从源码构建。$DOCS_INSTALL"
    fi
  else
    info "正在从源码构建服务端镜像。首次构建预计 5-15 分钟。"
    if ! "$ctl" build; then
      die "镜像构建失败。请看上面的输出。
    单独构建一个服务，可以不被进度流盖住看清报错：
      $ctl build spinneret"
    fi
  fi

  # Applied explicitly rather than relying on the depends_on chain. `migrate` is
  # a one-shot that compose can consider already satisfied when its container
  # spec has not changed, and "the schema is at whatever it was" is not a thing
  # an installer should leave to inference. The step is serialized across
  # instances by a PostgreSQL advisory lock, so running it twice is safe.
  info "正在执行数据库迁移。"
  "$ctl" run --rm migrate || die "迁移失败。请看上面的输出。
    现在还没有实例在提供服务，所以不存在升级到一半的状态。"

  info "正在拉起各个服务。"
  "$ctl" up -d --wait || die "这套编排没有健康启动。
    先问问它哪里不对：  $ctl ps
    再看日志：          $ctl logs --tail 100"
  ok "容器已启动。"
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
  step "等待这套编排报告就绪"
  dim "$url —— 冷启动还要初始化 PostgreSQL、让 ClickHouse 建好表结构、"
  dim "并让第一个副本把热状态建起来。"

  while [ "$waited" -lt "$READY_TIMEOUT" ]; do
    body="$(curl -fsS --max-time 5 "$url" 2>/dev/null || true)"
    case "$body" in
      *'"status":"ok"'*) ok "已就绪：$body"; return 0 ;;
    esac
    sleep 3
    waited=$(( waited + 3 ))
  done

  warn "等了 ${READY_TIMEOUT} 秒还没就绪。"
  if [ -n "$body" ]; then
    info "最后一次响应里点名了还没就绪的依赖："
    dim  "  $body"
    case "$body" in
      *'epoch missing'*)
        info "这一项有对应的命令：从 PostgreSQL 重建热状态。"
        dim  "  $INSTALL_DIR/spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild" ;;
      *'building'*)
        info "这一项在首次启动时是正常的，再等一会儿就好。" ;;
    esac
  else
    info "端口 $BIND_PORT 上完全没有任何响应。"
  fi
  die "编排起来了但还没就绪。什么都没丢，看看这两条：
      $INSTALL_DIR/spnrctl ps
      $INSTALL_DIR/spnrctl logs --tail 100 spinneret"
}

# Create the first administrator. Idempotent by design: the command short
# circuits and exits 0 when any user is already a platform admin, so re-running
# the installer over a live deployment does not disturb it.
bootstrap_admin() {
  step "创建第一个管理员"
  local ctl="$INSTALL_DIR/spnrctl"
  if ! "$ctl" --profile init run --rm init-admin; then
    die "创建管理员失败。编排本身在正常运行且健康，只有这一步失败了。
    单独再试一次：
      $ctl --profile init run --rm init-admin"
  fi
}

show_result() {
  local ctl="$INSTALL_DIR/spnrctl" shown_host="$BIND_HOST"
  [ "$shown_host" = "0.0.0.0" ] && shown_host="127.0.0.1"
  step "完成"

  printf '\n'
  printf '    %s控制台%s      %shttp://%s:%s%s\n' "$C_BOLD" "$C_RESET" "$C_GREEN" "$shown_host" "$BIND_PORT" "$C_RESET"
  printf '    %s用户名%s      %s\n' "$C_BOLD" "$C_RESET" "$ADMIN_USER"
  printf '    %s口令%s        %s\n' "$C_BOLD" "$C_RESET" "${ADMIN_PASSWORD:-<见 .env 里的 SPINNERET_ADMIN_PASSWORD>}"
  dim "第一次登录后请立刻改掉。在改掉之前，它还明文躺在 .env 里。"

  printf '\n'
  info "安装目录    $INSTALL_DIR"
  info "控制脚本    $ctl ps | logs -f spinneret | restart lb | down"
  info "管理菜单    bash install.zh.sh --manage"
  info "环境文件    $(compose_dir)/.env  （0600 —— 数据库口令在里面）"
  info "保险箱密钥  $(compose_dir)/secrets/kek.key  （务必备份；没有它什么都解不开）"

  printf '\n'
  info "接下来，在这台机器上："
  dim  "  # 给一个采集节点签发令牌"
  dim  "  $ctl run --rm -T --entrypoint /usr/local/bin/spnr migrate \\"
  dim  "      token create --name node-1 --scope lease:acquire --scope report:write --scope config:read"
  dim  ""
  dim  "  # 然后把节点指向服务端"
  dim  "  export SPINNERET_URL=http://$shown_host:$BIND_PORT"
  dim  "  export SPINNERET_TOKEN=<上面打印出来的令牌>"

  printf '\n'
  info "快速开始    $DOCS_ZH"
  info "Quickstart  $DOCS_EN"

  if [ "$WANT_OBSERVABILITY" = 1 ]; then
    printf '\n'
    info "Prometheus  http://127.0.0.1:${PROMETHEUS_PORT:-9090}  （仅回环，无鉴权）"
  fi
  if [ "$BIND_HOST" = "0.0.0.0" ]; then
    printf '\n'
    warn "这个控制台发布在所有网卡上，而且没有 TLS；/metrics 还和它共用同一个"
    warn "监听端口，完全没有鉴权。在它能被外面访问到之前，先在前面架一个"
    warn "反向代理。"
  fi
  printf '\n'
  warn "现在就去备份 $(compose_dir)/secrets/kek.key，趁还没有数据可丢。"
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
  [ -n "$TTY_IN" ] || { warn "没有终端可以读取口令。"; return 1; }
  while true; do
    printf '    新口令（至少 10 个字符）：' >&2
    IFS= read -rs first <"$TTY_IN" || first=""
    printf '\n' >&2
    if [ -z "$first" ]; then
      warn "已取消。"
      return 1
    fi
    if [ "${#first}" -lt 10 ]; then
      warn "太短了。"
      continue
    fi
    printf '    再输一次：' >&2
    IFS= read -rs second <"$TTY_IN" || second=""
    printf '\n' >&2
    if [ "$first" != "$second" ]; then
      warn "两次输入不一致。"
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
  printf '    输入 %s 确认，输入其他任何内容即取消：' "$word" >&2
  # Trimmed like every other prompt in this script: a trailing space silently
  # cancelling a destructive action is worse than accepting one.
  reply="$(trim "$(read_line)")"
  [ "$reply" = "$word" ]
}

pause_for_reader() {
  [ -n "$TTY_IN" ] || return 0
  printf '\n    按回车返回。' >&2
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
    API_COOKIES="$(mktemp -t spinneret-session.XXXXXX)" || { warn "无法创建临时文件。"; return 1; }
    TMP_FILES+=("$API_COOKIES")
    chmod 600 "$API_COOKIES" 2>/dev/null || true
  fi
  : >"$API_COOKIES"
  body="$(printf '{"username":"%s","password":"%s"}' "$(json_escape "$API_USER")" "$(json_escape "$API_PASSWORD")")"
  if ! response="$(printf '%s' "$body" | api_call AuthService Login)"; then
    warn "$API_USER 登录失败。要么口令不对，要么控制台没有响应。"
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
  API_USER="$(ask_value "管理员用户名？" "${ADMIN_USER:-admin}")"
  recorded="$(env_get SPINNERET_ADMIN_PASSWORD)"
  if [ -n "$recorded" ]; then
    API_PASSWORD="$(read_password_once "口令（留空则用 .env 里记录的那个）：" || true)"
    [ -n "$API_PASSWORD" ] || API_PASSWORD="$recorded"
  else
    API_PASSWORD="$(read_password_once "口令：" || true)"
  fi
  [ -n "$API_PASSWORD" ] || { warn "没有输入口令。"; return 1; }
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
  step "状态"
  local installed latest
  installed="$(running_version || true)"
  if [ -n "$installed" ]; then
    ok "运行中的版本 $installed"
  else
    warn "没有在运行，或者没有副本应答。"
  fi
  if [ "$USE_PUBLISHED" = 1 ]; then
    ok "镜像 $IMAGE_REPO:$IMAGE_TAG"
  else
    ok "由 $INSTALL_DIR 里的源码构建"
  fi
  ok "发布在 $BIND_HOST:${BIND_PORT}，$REPLICAS 个副本"

  printf '\n'
  "$CTL" ps --format "table {{.Service}}\t{{.Status}}\t{{.Ports}}" 2>/dev/null | sed 's/^/    /' || true

  printf '\n'
  info "表结构版本："
  spnr_run migrate status 2>&1 | sed 's/^/      /' || warn "读不到表结构版本。"

  printf '\n'
  info "Docker 占用的磁盘："
  docker system df 2>/dev/null | sed 's/^/      /' || true

  latest="$(latest_release)"
  # Straight off the network, so checked before it is shown or offered.
  case "$latest" in ''|[!A-Za-z0-9_]*|*[!A-Za-z0-9._-]*) latest="" ;; esac
  if [ -n "$latest" ] && [ -n "$installed" ] && version_is_newer "$latest" "$installed"; then
    printf '\n'
    warn "$latest 已经发布，这个实例还在 ${installed}。"
  elif [ -n "$latest" ]; then
    printf '\n'
    ok "已是最新发布版本（${latest}）。"
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
    [ "$count" = 1 ] && dim "只有 1 个副本，所以这是一次有空窗的重启，不是滚动重启。"
    "$CTL" up -d --wait || return 1
    return 0
  fi

  info "将 $count 个副本逐个滚动替换。"
  for id in $ids; do
    info "正在替换 ${id:0:12}"
    docker rm -f "$id" >/dev/null 2>&1 || true
    if ! "$CTL" up -d --no-recreate --wait --no-deps spinneret; then
      warn "单独替换这个副本没成功，改成整体重建这个服务。"
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
  valid_image_tag "$target" || { warn "这不是一个可用的镜像 tag；什么都没有改动。"; return 0; }
  step "升级到 $target"
  info "你的数据不会被动：命名数据卷会保留下来。"
  dim  "如果最近没备份过，先备份 —— 菜单第 10 项一步搞定。"
  printf '\n'

  if [ "$USE_PUBLISHED" = 1 ]; then
    # Pull BEFORE pinning. Writing the tag first and then failing to fetch it
    # leaves the deployment pointing at an image that does not exist: the stack
    # keeps running on what is already up, but the next `up -d` cannot start.
    # The tag goes on this pull's environment only; .env is not touched until
    # the image is known to be there.
    info "正在拉取 $(resolve_image_ref "$target")。"
    if ! SPINNERET_IMAGE_TAG="$target" "$CTL" pull migrate spinneret; then
      die "拉取 $(resolve_image_ref "$target") 失败。什么都没改，旧版本还在正常运行。
    确认这个 tag 是否存在：
      https://github.com/TikHub/Spinneret/pkgs/container/spinneret"
    fi
    env_set SPINNERET_IMAGE_TAG "$target" || warn "无法把 tag 写进 .env。"
    IMAGE_TAG="$target"
    ok "已固定 SPINNERET_IMAGE_TAG=$target"
  else
    info "正在更新代码检出。"
    git -C "$INSTALL_DIR" fetch --quiet origin main || warn "拉取失败，就用当前检出继续。"
    git -C "$INSTALL_DIR" merge --ff-only --quiet origin/main 2>/dev/null \
      || warn "检出里有本地改动，保持原样不动。"
    info "正在重新构建，预计几分钟。"
    "$CTL" build || die "重新构建失败。没有做任何切换，旧容器还在跑。"
  fi

  info "正在执行迁移。"
  "$CTL" run --rm migrate \
    || die "迁移失败。旧容器还在跑，也没有做任何切换。
    回滚请用备份恢复，不要用 'migrate down' —— 向下迁移会删表，连表里的数据
    一起删掉。"

  info "正在重启。"
  rolling_restart || die "新版本没有健康启动。试试：$CTL logs --tail 100 spinneret"

  printf '\n'
  spnr_run migrate status 2>&1 | sed 's/^/    /' || true
  ok "当前版本 $(running_version || echo "$target")。"
}

act_change_password() {
  step "修改管理员口令"
  dim "这一步会以该账号的身份登录，然后改它自己的口令 —— 和在控制台里做的一样。"
  dim "该账号的其他所有会话都会被终止。"
  printf '\n'
  api_prompt_credentials || return 0

  local new_password body
  new_password="$(read_password_twice)" || return 0
  body="$(printf '{"currentPassword":"%s","newPassword":"%s"}' \
    "$(json_escape "$API_PASSWORD")" "$(json_escape "$new_password")")"
  if printf '%s' "$body" | api_call AuthService ChangePassword >/dev/null; then
    ok "$API_USER 的口令已修改。"
    # SPINNERET_ADMIN_PASSWORD is only read by the one-shot init-admin, but a
    # stale value there is a trap for the next person who reads the file.
    if [ "$API_USER" = "$(env_get SPINNERET_ADMIN_USERNAME)" ]; then
      printf '\n'
      dim ".env 里记录的还是这个账号的旧口令。"
      if ask_yes_no "把 .env 里的 SPINNERET_ADMIN_PASSWORD 一并更新吗？" "y"; then
        if env_set SPINNERET_ADMIN_PASSWORD "$new_password"; then
          ok "已更新 .env。"
        else
          warn "无法更新 .env，里面还是旧值。"
        fi
      else
        warn "保留 .env 里的旧值。它现在是错的。"
      fi
    fi
  else
    warn "没有成功。新口令至少要 10 个字符。"
  fi
  unset new_password
  API_PASSWORD=""
}

act_add_admin() {
  step "新增管理员"
  dim "在当前租户里创建一个带 admin 角色的控制台账号。"
  dim "账号不支持改名：只能创建账号和重置口令。"
  printf '\n'
  api_prompt_credentials || return 0
  if [ -z "$API_TENANT" ]; then
    warn "无法确定该把账号创建在哪个租户下。"
    info "请改到控制台里创建：访问控制 → 用户 → 新建。"
    return 0
  fi

  local who new_password body
  who="$(ask_value "新账号名？" "" valid_username)"
  [ -n "$who" ] || { warn "没有输入名字。"; return 0; }
  new_password="$(read_password_twice)" || return 0
  body="$(printf '{"username":"%s","displayName":"%s","password":"%s","role":"admin"}' \
    "$(json_escape "$who")" "$(json_escape "$who")" "$(json_escape "$new_password")")"
  if printf '%s' "$body" | api_call AccessAdminService CreateUser >/dev/null; then
    ok "已创建 ${who}，角色为 admin。"
    dim "admin 是租户级的，不是平台级的：它管不了租户本身，也管不了密钥加密密钥。"
    dim "如果确实需要，请在控制台里另外授权。"
  else
    warn "没有成功。可能是名字已被占用，或者口令太短。"
  fi
  unset new_password
  API_PASSWORD=""
}

act_list_users() {
  step "账号列表"
  api_prompt_credentials || return 0
  local response parsed
  response="$(printf '{"allUsers":true,"pageSize":200}' | api_call AccessAdminService ListUsers || true)"
  API_PASSWORD=""
  if [ -z "$response" ]; then
    warn "无法列出账号。"
    return 0
  fi
  parsed="$(printf '%s' "$response" | parse_users || true)"
  if [ -n "$parsed" ]; then
    printf '\n'
    # printf pads by bytes, not by display width. The three-character Chinese
    # heading is nine bytes wide, so the field width is 28 + 3 to line up with
    # the 28-column data rows parse_users prints.
    printf '    %-31s %-30s %s\n' "用户名" "ID" "标记"
    printf '%s\n' "$parsed" | sed 's/^/    /'
  else
    warn "从响应里没解析出任何账号。原始响应："
    # head last, and the whole pipeline guarded. With `head -c` in the middle it
    # closes the pipe on printf, and that SIGPIPE under pipefail and errexit
    # would take the management session down on exactly the error path this
    # branch exists to serve.
    printf '%s\n' "$response" | sed 's/^/    /' | head -c 2000 || true
    printf '\n'
  fi
}

act_create_token() {
  step "创建 API 令牌"
  dim "节点用的令牌。明文只打印一次，之后再也取不回来，所以现在就复制走。"
  dim "作用域（scope）用英文逗号分隔："
  dim "  lease:acquire[:<站点>]  report:write[:<站点>]  config:read[:<配置组 glob>]"
  dim "  config:publish[:<配置组 glob>]  secret:read:<命名空间>/<路径 glob>"
  dim "  identity:write[:<站点>]  proxy:write  admin"
  printf '\n'

  local name tenant namespace scopes expires description
  name="$(ask_value "令牌名称（在命名空间内唯一）？" "")"
  [ -n "$name" ] || { warn "没有输入名称。"; return 0; }
  tenant="$(ask_value "租户？" "default")"
  namespace="$(ask_value "命名空间？" "default")"
  scopes="$(ask_value "作用域？" "lease:acquire,report:write,config:read")"
  expires="$(ask_value "有效期（720h、30d，或 never）？" "720h")"
  description="$(ask_value "描述（可留空）？" "")"

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
    dim "上面那一行就是令牌。它不会被存在任何你能读到的地方。"
  else
    warn "令牌没有创建成功。上面的输出说明了原因。"
  fi
}

act_rebuild() {
  step "重建热状态"
  dim "从 PostgreSQL 重建 Redis 里的工作集。Valkey 数据卷被重建或清空之后需要做"
  dim "这一步，它的表现是 /readyz 报"
  dim "\"hotstate: epoch missing (rebuild pending)\"。"
  printf '\n'
  warn "不指定站点时，这一步会先删掉 epoch 键，于是所有副本都会报未就绪 ——"
  warn "在它跑完之前相当于全集群中断。"
  warn "指定一个站点可以避免这种情况，只重建那个站点。"
  printf '\n'
  local site
  site="$(ask_value "哪个站点（留空 = 整个集群）？" "")"
  if [ -z "$site" ]; then
    confirm_word "rebuild" || { info "已取消。"; return 0; }
    spnr_run rebuild 2>&1 | sed 's/^/    /' || warn "重建失败。请看上面的输出。"
    return 0
  fi
  local tenant namespace
  tenant="$(ask_value "租户？" "default")"
  namespace="$(ask_value "命名空间？" "default")"
  spnr_run rebuild --tenant "$tenant" --namespace "$namespace" --site "$site" 2>&1 | sed 's/^/    /' \
    || warn "重建失败。请看上面的输出。"
}

act_show_config() {
  step "配置"
  dim "打印服务端将会加载的那套 SPINNERET_* 环境变量，已做校验，所有凭据都被替换掉。"
  dim "手改过 .env 之后，用这一步做起飞前检查。"
  printf '\n'
  spnr_run config check 2>&1 | sed 's/^/    /' || warn "配置校验没通过。每条报错都会点名对应的变量。"
}

act_diagnose() {
  step "健康检查"
  local live ready

  live="$(curl -fsS --max-time 5 "http://127.0.0.1:$BIND_PORT/healthz" 2>/dev/null || true)"
  if [ -n "$live" ]; then ok "存活探针  $live"; else warn "存活探针  端口 $BIND_PORT 上没有任何响应"; fi

  ready="$(curl -sS --max-time 5 "http://127.0.0.1:$BIND_PORT/readyz" 2>/dev/null || true)"
  case "$ready" in
    *'"status":"ok"'*)       ok   "就绪探针  $ready" ;;
    *'"status":"draining"'*) warn "就绪探针  $ready  —— 有副本正在重启；持续约 5 秒属于正常" ;;
    "")                      warn "就绪探针  没有任何响应" ;;
    *)                       warn "就绪探针  $ready" ;;
  esac
  case "$ready" in
    *'epoch missing'*) info "  处理办法：菜单第 5 项，重建热状态。" ;;
    *'postgres'*'unreachable'*) info "  处理办法：$CTL logs --tail 40 postgres" ;;
    *'building'*) info "  刚启动或刚重建完时属于正常，等一会儿。" ;;
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
      warn "$svc 被 OOM 杀过。这台机器的内存撑不起默认编排。"
    else
      ok "$svc 没有被 OOM 杀过。"
    fi
  done
}

act_logs() {
  step "日志"
  dim "每个服务的最后 60 行。想实时跟随：$CTL logs -f spinneret"
  printf '\n'
  "$CTL" logs --tail 60 2>&1 | tail -120 || true
}

act_restart() {
  step "重启"
  if ask_yes_no "把服务端副本逐个滚动重启吗？" "y"; then
    rolling_restart || warn "重启失败。"
  else
    "$CTL" restart || warn "重启失败。"
  fi
  printf '\n'
  "$CTL" ps --format "table {{.Service}}\t{{.Status}}" 2>/dev/null | sed 's/^/    /' || true
}

# A backup is three things, and it is only a backup if it is all three: the
# PostgreSQL dump, the key-encryption key that decrypts what is in it, and the
# .env whose passwords match the volumes. Valkey is derived from PostgreSQL and
# comes back with `spnr rebuild`; ClickHouse holds analytics that expire anyway.
act_backup() {
  step "备份"
  local dir stamp target old_umask
  dir="$INSTALL_DIR/backups"
  stamp="$(date -u '+%Y%m%dT%H%M%SZ')"
  target="$dir/$stamp"
  mkdir -p "$target" || { warn "无法创建 ${target}。"; return 0; }

  dim "备份写到 $target —— 就在安装目录里面，所以整目录卸载会把它一起带走。"
  dim "请把它复制到这台机器之外。"
  printf '\n'

  old_umask="$(umask)"
  umask 077
  info "正在导出 PostgreSQL。"
  if ! "$CTL" exec -T postgres pg_dump -U spinneret -Fc spinneret >"$target/postgres.dump"; then
    umask "$old_umask"
    warn "导出失败。请看上面的输出。"
    rm -rf -- "$target"
    return 0
  fi
  umask "$old_umask"

  # 0600 on the copy. act_restore installs it back with an explicit
  # `install -m 0644`, which sets the destination's mode whatever the source's
  # was, so nothing downstream needs this copy to be readable by anyone else -
  # and this one lands in a backups tree that gets copied around.
  install -m 0600 "$(compose_dir)/secrets/kek.key" "$target/kek.key" 2>/dev/null \
    || warn "无法复制 kek.key。只有数据导出是打不开的。"
  install -m 0600 "$(compose_dir)/.env" "$target/env" 2>/dev/null \
    || warn "无法复制 .env。"

  ok "已写入 $target"
  find "$target" -maxdepth 1 -type f -exec ls -l {} + 2>/dev/null | sed 's/^/      /' || true
  printf '\n'
  warn "在它被复制出这台机器、并且你至少往一套空编排里成功恢复过一次之前，"
  warn "这都不算备份。没有别的办法能证明它可用。"
}

act_restore() {
  step "恢复"
  local dir
  dir="$INSTALL_DIR/backups"
  if [ ! -d "$dir" ] || [ -z "$(ls -A "$dir" 2>/dev/null || true)" ]; then
    warn "$dir 里没有备份。"
    info "如果备份是从别处拷回这台机器的，先把它的目录（postgres.dump 加 kek.key）"
    info "放到那里，这一步就能认出来。"
    return 0
  fi
  find "$dir" -mindepth 1 -maxdepth 1 -exec basename {} \; 2>/dev/null | sort | sed 's/^/      /'
  printf '\n'
  warn "恢复会替换掉当前的数据库。现在里面的东西会全部消失。"
  warn "而且它只在密钥加密密钥和当初导出时用的那一把一致时才有效：不一致的表现是"
  warn "\"is the KEK the one used to initialize this database?\"，"
  warn "除了换成正确的密钥，没有别的解法。"
  printf '\n'
  local which src
  which="$(ask_value "恢复哪一份（填上面列出的名字）？" "")"
  [ -n "$which" ] || return 0
  src="$dir/$which"
  [ -f "$src/postgres.dump" ] || { warn "$src/postgres.dump 不存在。"; return 0; }
  confirm_word "restore" || { info "已取消。"; return 0; }

  if [ -f "$src/kek.key" ]; then
    if ! cmp -s "$src/kek.key" "$(compose_dir)/secrets/kek.key"; then
      warn "这份备份里的密钥和当前部署在用的不是同一把。"
      warn "替换正在用的密钥是本脚本里唯一不可逆的一步：已经用当前密钥加密的东西"
      warn "仍然只能用它解开，而之后只有下面另存的那份带时间戳的副本还能打开它们。"
      if ask_yes_no "用这份备份里的密钥替换正在用的那把吗？" "n" \
         && confirm_word "replace-key"; then
        # Never overwritten. A fixed kek.key.previous means a second restore
        # destroys the key the first one saved, and every dump ever taken under
        # that key becomes unopenable by anyone.
        local saved
        saved="$(compose_dir)/secrets/kek.key.$(date -u '+%Y%m%dT%H%M%SZ').previous"
        cp -p "$(compose_dir)/secrets/kek.key" "$saved" \
          || { warn "无法备份当前密钥，保持原样不动。"; return 0; }
        install -m 0644 "$src/kek.key" "$(compose_dir)/secrets/kek.key"
        ok "已装上备份里的密钥；旧的那把存成了 $saved。"
      else
        warn "保留当前密钥。这次恢复极有可能打不开。"
      fi
    fi
  else
    warn "这份备份里没有 kek.key。如果它来自另一套部署，恢复出来的数据行"
    warn "是解不开的。"
  fi

  # The replicas hold open connections to the objects `pg_restore --clean` is
  # about to drop, and a DROP on an object still in use fails. Stopping them
  # first is what makes the restore clean rather than partial; the
  # rolling_restart below brings them back.
  info "先停掉服务端副本，避免恢复过程和它们抢数据库。"
  "$CTL" stop spinneret >/dev/null 2>&1 || true

  info "正在恢复。"
  if ! "$CTL" exec -T postgres pg_restore -U spinneret -d spinneret --clean --if-exists <"$src/postgres.dump"; then
    warn "pg_restore 报了错。在相信这次恢复之前，请先看上面的输出。"
  fi

  # Mandatory, not optional: the hot state in Valkey describes the database that
  # was there a moment ago. Until it is rebuilt every replica reports
  # "hotstate: epoch missing" and the load balancer serves nothing.
  info "正在用恢复后的数据库重建热状态。"
  spnr_run rebuild 2>&1 | sed 's/^/    /' || warn "重建失败；在相信这次恢复之前，请先跑菜单第 5 项。"

  info "正在重启。"
  rolling_restart || warn "编排没有健康恢复。试试：$CTL logs --tail 100 spinneret"
  act_diagnose
}

# Disk. Scoped to this project's own images on purpose: a plain
# `docker image prune -a` would take unrelated images off a host that runs other
# stacks, which is not this script's to decide. The build-cache option below is
# the exception - BuildKit's cache is per host and its records carry no project
# label - and it says so where it is offered.
act_free_disk() {
  step "清理磁盘"
  info "清理前："
  docker system df 2>/dev/null | sed 's/^/      /' || true

  printf '\n'
  dim "这里会无上限增长的只有两样：旧的镜像 tag 和构建缓存。"
  dim "ClickHouse 的数据卷也会涨，但它自己按 TTL 过期数据行，"
  dim "所以手动删它不是正确做法。"
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
    info "本项目的镜像里，没有任何容器引用的有："
    printf '%s\n' "$images" | sed 's/^/      /'
    printf '\n'
    if ask_yes_no "删掉它们吗？" "n"; then
      printf '%s\n' "$images" | awk '{print $2}' | sort -u | while read -r id; do
        docker rmi "$id" >/dev/null 2>&1 && info "已删除 $id" || true
      done
    fi
  else
    ok "本项目没有残留的旧镜像。"
  fi

  printf '\n'
  dim "构建缓存的意义就是：改一小处之后重新构建只要几秒，而不是十分钟。"
  dim "BuildKit 的缓存是整台机器共用的，缓存记录上也没有项目标签，没法只清掉"
  dim "本项目那一份：清的是整台机器的，之后这台机器上任何项目的第一次构建都是冷的。"
  if ask_yes_no "清掉这台机器上全部的构建缓存吗？" "n"; then
    docker builder prune -af 2>&1 | tail -2 | sed 's/^/    /' || true
  fi

  printf '\n'
  info "清理后："
  docker system df 2>/dev/null | sed 's/^/      /' || true
}

# ----------------------------------------------------------------- menus --

menu_uninstall() {
  while true; do
    step "停止或移除"
    info "  1  停掉这套编排，数据一个字节都不动"
    info "  2  停掉并删除数据卷 —— PostgreSQL、Valkey、ClickHouse"
    info "  3  在 2 的基础上，再删掉 ${INSTALL_DIR}，包括密钥加密密钥"
    info "  b  返回"
    case "$(ask_choice "选哪个？")" in
      1)
        "$CTL" down --remove-orphans && ok "已停止。再启动请用：$CTL up -d --wait"
        return 0 ;;
      2)
        printf '\n'
        warn "这会删掉 PostgreSQL 数据卷 —— 这套部署里的每一个身份、代理、策略、"
        warn "配置、密钥、用户和令牌。"
        dim  "$(compose_dir)/secrets/kek.key 里的密钥加密密钥会保留，"
        dim  "所以用它导出的备份还能恢复到一套全新的编排里。"
        confirm_word "delete" || { info "已取消。"; continue; }
        "$CTL" down -v --remove-orphans && ok "已停止，数据卷也删掉了。"
        return 0 ;;
      3)
        printf '\n'
        warn "这会删掉数据卷，并且删掉 ${INSTALL_DIR}。"
        warn "那个目录里有 .env 和 secrets/kek.key。没有那把密钥，这套部署的任何"
        warn "备份都永远打不开了 —— 任何人都打不开。"
        printf '\n'
        if [ -f "$(compose_dir)/secrets/kek.key" ] \
           && ask_yes_no "要先把密钥打印出来，好让你抄走吗？" "n"; then
          printf '\n'
          cat "$(compose_dir)/secrets/kek.key" | sed 's/^/    /'
          printf '\n'
          pause_for_reader
        fi
        confirm_word "delete" || { info "已取消。"; continue; }
        "$CTL" down -v --remove-orphans || true
        # Guarded rather than trusting the variable: this is the one place the
        # script removes a tree, and an empty or silly value must not reach rm.
        case "$INSTALL_DIR" in
          ""|"/"|"/usr"|"/etc"|"/var"|"/home"|"/root"|"/opt"|"/bin"|"/sbin"|"/lib"|"/boot")
            die "拒绝删除 ${INSTALL_DIR}。" ;;
        esac
        [ -f "$INSTALL_DIR/deploy/compose/docker-compose.yml" ] \
          || die "$INSTALL_DIR 看起来不像一套安装，不删它。"
        rm -rf -- "$INSTALL_DIR" && ok "已删除 ${INSTALL_DIR}。"
        info "Docker 镜像不动；本脚本不会在一台共用的机器上做清理。"
        return 0 ;;
      b|"") return 0 ;;
      *) warn "请选 1、2、3 或 b。" ;;
    esac
  done
}

menu_manage() {
  while true; do
    step "管理"
    info "  1  修改管理员口令   7  健康检查"
    info "  2  新增管理员       8  查看日志"
    info "  3  列出账号         9  重启服务"
    info "  4  创建 API 令牌   10  立即备份"
    info "  5  重建热状态      11  恢复备份"
    info "  6  查看配置        12  清理磁盘"
    info "  b  返回"
    case "$(ask_choice "选哪个？")" in
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
      *) warn "请按列表选一个数字，或者 b。" ;;
    esac
  done
}

menu_existing() {
  local installed latest upgrade_line=""
  installed="$(running_version || true)"
  latest="$(latest_release)"
  # Straight off the network, so checked before it is shown or offered.
  case "$latest" in ''|[!A-Za-z0-9_]*|*[!A-Za-z0-9._-]*) latest="" ;; esac

  step "这里已经有一套安装了"
  ok "$INSTALL_DIR"
  if [ -n "$installed" ]; then
    ok "运行中 $installed"
  else
    warn "没有在运行，或者还没有副本应答。"
  fi
  if [ "$USE_PUBLISHED" = 1 ]; then
    ok "镜像 $IMAGE_REPO:$IMAGE_TAG"
  else
    ok "由源码构建"
  fi
  if [ -n "$latest" ] && [ -n "$installed" ] && version_is_newer "$latest" "$installed"; then
    upgrade_line="$latest"
    warn "$latest 已经可用。"
  fi

  while true; do
    printf '\n'
    info "  1  状态 —— 版本、容器、表结构、磁盘"
    if [ -n "$upgrade_line" ]; then
      info "  2  升级到 $upgrade_line"
    elif [ "$USE_PUBLISHED" = 1 ]; then
      info "  2  切换到别的镜像 tag（重新拉取、迁移、重启）"
    else
      info "  2  用最新源码重新构建（拉取、构建、迁移、重启）"
    fi
    info "  3  管理 —— 账号、令牌、备份、健康、磁盘"
    info "  4  停止或移除这套安装"
    info "  q  退出"
    case "$(ask_choice "选哪个？")" in
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
          target="$(ask_value "用哪个 tag？" "$upgrade_line" valid_image_tag)"
        else
          # No newer release does not mean nowhere to go. The same field takes
          # an older tag, which is how a rollback is done.
          dim "没有更新的发布版本。你仍然可以切到别的已发布 tag，"
          dim "包括切回一个更旧的 tag 来回滚。"
          target="$(ask_value "用哪个 tag？" "$IMAGE_TAG" valid_image_tag)"
        fi
        if [ -n "$target" ]; then
          [ "$target" = "$IMAGE_TAG" ] && info "已经在 $target 上了。还是重新拉取并重启一次。"
          do_upgrade "$target"
        fi
        pause_for_reader ;;
      3) menu_manage ;;
      4) menu_uninstall; return 0 ;;
      q|"") return 0 ;;
      *) warn "请选 1、2、3、4 或 q。" ;;
    esac
  done
}

# -------------------------------------------------------------------- main --

usage() {
  cat <<EOF
用法：bash install.zh.sh [选项]

  --yes, -y      所有问题都取默认答案。发布在 127.0.0.1:8080，一个叫 admin 的
                 管理员，副本数按这台机器换算，可观测性关闭。
  --check        只探测系统并打印将会发生什么，然后停下。不改任何东西、不写任何
                 文件、不问任何问题。
  --manage       直接进入已有安装的管理菜单。
  --help, -h     这段说明。

不带任何选项运行时它会自己判断该做哪件事：这里没装过就安装，装过就打开菜单 ——
状态、升级、账号、令牌、备份、健康检查、磁盘、卸载。

环境变量（每一个都能预设一个答案，配合 --yes 就是无人值守安装）：
  SPINNERET_PROJECT          Compose 项目名。默认：spinneret。
  SPINNERET_INSTALL_DIR      装到哪里。默认：root 是 /opt/spinneret，
                             其他用户是 ~/spinneret。
  SPINNERET_BIND_HOST        127.0.0.1（默认）或 0.0.0.0。
  SPINNERET_PORT             对外发布的端口。默认：8080。
  SPINNERET_ADMIN_USERNAME   第一个管理员。默认：admin。
  SPINNERET_REPLICAS         服务端副本数。默认：按 CPU 和内存换算。
  SPINNERET_ENABLE_OBSERVABILITY  1 加上 Prometheus，0（默认）不加。
  SPINNERET_USE_PUBLISHED    1（默认）拉已发布镜像，0 从源码构建。
  SPINNERET_IMAGE            镜像仓库。默认：${DEFAULT_IMAGE}。
  SPINNERET_IMAGE_TAG        镜像 tag。默认：latest。
  NO_COLOR                   设成任意值即关闭彩色输出。

管理员口令和数据库口令一律在本机生成，绝不从环境变量里读取。

文档：$DOCS_ZH
      $DOCS_EN
EOF
}

main() {
  while [ $# -gt 0 ]; do
    case "$1" in
      -y|--yes)  ASSUME_YES=1 ;;
      --check)   CHECK_ONLY=1 ;;
      --manage)  MANAGE_ONLY=1 ;;
      -h|--help) usage; exit 0 ;;
      *)         die "无法识别的选项：$1（试试 --help）" ;;
    esac
    shift
  done

  banner
  detect_os
  detect_resources
  report_host

  step "检查 Docker"
  if ! check_docker_present; then
    if [ "$CHECK_ONLY" = 1 ]; then
      warn "没有安装 Docker。正式运行时脚本会问你要不要装。"
      info "这个系统对应的包管理器命令："
      dim  "  $(docker_install_hint)"
      exit 0
    fi
    offer_docker_install
  fi
  ok "Docker $(docker --version 2>/dev/null | sed 's/Docker version //; s/,.*//')"

  if [ "$CHECK_ONLY" = 1 ]; then
    check_compose || true
    if check_docker_running; then ok "Docker 守护进程有响应。"; else warn "Docker 守护进程没有响应。"; fi
    for tool in git curl; do
      if have "$tool"; then ok "$tool"; else warn "缺少 ${tool}：$(pkg_install_hint "$tool")"; fi
    done
    if image_available "$IMAGE_REPO:$IMAGE_TAG"; then
      ok "$IMAGE_REPO:$IMAGE_TAG 可以拉取。"
    else
      warn "拉不到 $IMAGE_REPO:${IMAGE_TAG}；正式安装时会改成从源码构建。"
    fi
    step "仅检查"
    info "什么都没改。去掉 --check 再跑就会真正安装。"
    exit 0
  fi

  ensure_docker_running
  check_compose || die "需要 Compose v2 $COMPOSE_MIN 或更高版本。"
  have curl || die "没有安装 curl。用 $(pkg_install_hint curl) 装上，然后再跑一次本脚本。"

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
    die "没有找到项目 '$PROJECT' 的安装。
    不带 --manage 跑一次本脚本来创建一套，或者把 SPINNERET_INSTALL_DIR
    设成它所在的目录。"
  fi

  if [ -n "$found" ] && [ -x "$found/spnrctl" ]; then
    INSTALL_DIR="$found"
    CTL="$found/spnrctl"
    load_install_settings
    if [ "$MANAGE_ONLY" = 0 ] && [ "$ASSUME_YES" = 1 ]; then
      # --yes means "take the default answer to every question", and over a
      # deployment that already exists the default answer to "change the image
      # source, the profile set and the published port" is no. So: nothing.
      step "这里已经有一套安装了"
      ok "$INSTALL_DIR"
      info "什么都没有改动。--yes 会用默认值回答所有问题，而对一套已经在跑的"
      info "部署来说，那些默认值并不是它的设置。要操作这一套，请用："
      info "  bash install.zh.sh --manage     或者：$CTL ps"
      exit 0
    fi
    if [ -z "$TTY_IN" ]; then
      warn "$INSTALL_DIR 里已经有一套安装了。"
      info "想管理它请在终端里跑本脚本，或者直接用 ${CTL}。"
      exit 0
    fi
    menu_existing
    exit 0
  fi

  if [ -z "$TTY_IN" ] && [ "$ASSUME_YES" = 0 ]; then
    die "没有终端可以提问。
    要么先把脚本下载下来再运行：
      curl -fsSL $REPO_RAW/install/install.zh.sh -o install.zh.sh && bash install.zh.sh
    要么全部接受默认答案：
      curl -fsSL $REPO_RAW/install/install.zh.sh | bash -s -- --yes"
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
