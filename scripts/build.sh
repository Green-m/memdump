#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly OUTPUT_DIR="${PROJECT_DIR}/bin"

readonly DEFAULT_TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "linux/386"
  "linux/arm/v7"
  "linux/riscv64"
  "linux/loong64"
)

usage() {
  cat <<'EOF'
用法: scripts/build.sh [目标 ...]

目标格式为 GOOS/GOARCH 或 GOOS/GOARCH/变体，例如：
  scripts/build.sh linux/amd64 linux/arm64 linux/arm/v7

不指定目标时构建：
  linux/amd64 linux/arm64 linux/386 linux/arm/v7 linux/riscv64 linux/loong64

所有产物写入 bin/，同时生成 bin/SHA256SUMS。
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if (($# > 0)); then
  targets=("$@")
else
  targets=("${DEFAULT_TARGETS[@]}")
fi

command -v go >/dev/null 2>&1 || {
  echo "错误: 找不到 go 命令" >&2
  exit 1
}

mkdir -p -- "${OUTPUT_DIR}"

artifacts=()
for target in "${targets[@]}"; do
  IFS=/ read -r goos goarch variant extra <<<"${target}"
  if [[ -z "${goos}" || -z "${goarch}" || -n "${extra:-}" ]]; then
    echo "错误: 无效的构建目标 ${target}" >&2
    exit 2
  fi
  if [[ "${goos}" != "linux" ]]; then
    echo "错误: memdump 依赖 Linux /proc，不支持目标 ${target}" >&2
    exit 2
  fi
  if [[ -n "${variant:-}" && "${goarch}" != "arm" ]]; then
    echo "错误: 只有 linux/arm 目标支持第三段变体: ${target}" >&2
    exit 2
  fi

  suffix="${goos}-${goarch}"
  build_env=("CGO_ENABLED=0" "GOOS=${goos}" "GOARCH=${goarch}")
  if [[ "${goarch}" == "arm" ]]; then
    goarm="${variant:-v7}"
    goarm="${goarm#v}"
    if [[ ! "${goarm}" =~ ^[5-7]$ ]]; then
      echo "错误: GOARM 必须是 5、6 或 7: ${target}" >&2
      exit 2
    fi
    suffix="${suffix}-v${goarm}"
    build_env+=("GOARM=${goarm}")
  fi

  output="${OUTPUT_DIR}/memdump-${suffix}"
  echo "==> 构建 ${target} -> ${output#"${PROJECT_DIR}/"}"
  (
    cd -- "${PROJECT_DIR}"
    env "${build_env[@]}" go build \
      -buildvcs=false \
      -trimpath \
      -ldflags="-s -w" \
      -o "${output}" \
      .
  )
  artifacts+=("${output}")
done

checksum_file="${OUTPUT_DIR}/SHA256SUMS"
(
  cd -- "${OUTPUT_DIR}"
  checksum_names=()
  for artifact in "${artifacts[@]}"; do
    checksum_names+=("${artifact##*/}")
  done
  sha256sum -- "${checksum_names[@]}" >"${checksum_file}"
)

echo "==> 完成，共生成 ${#artifacts[@]} 个二进制文件"
echo "==> 校验文件: ${checksum_file#"${PROJECT_DIR}/"}"
