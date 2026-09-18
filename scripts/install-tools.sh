#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: install-tools.sh <installation-root>" >&2
  exit 2
fi
if [ "${RUNNER_OS:-Linux}" != "Linux" ] || [ "${RUNNER_ARCH:-X64}" != "X64" ]; then
  echo "CloudForge's GitHub Action currently supports Ubuntu x64 runners." >&2
  exit 2
fi

install_root="$1"
bin_dir="$install_root/bin"
download_dir="$(mktemp -d "${RUNNER_TEMP:-/tmp}/cloudforge-tools.XXXXXX")"
trap 'rm -rf "$download_dir"' EXIT
mkdir -p "$bin_dir"

download() {
  curl --fail --silent --show-error --location --connect-timeout 15 --max-time 300 --retry 3 --retry-delay 2 --output "$2" "$1"
}

cd "$download_dir"

download "https://github.com/k3d-io/k3d/releases/download/v5.9.0/k3d-linux-amd64" k3d-linux-amd64
download "https://github.com/k3d-io/k3d/releases/download/v5.9.0/checksums.txt" k3d-checksums.txt
k3d_checksum="$(awk '$2 == "_dist/k3d-linux-amd64" {print $1}' k3d-checksums.txt)"
test -n "$k3d_checksum"
echo "$k3d_checksum  k3d-linux-amd64" | sha256sum --check
install -m 0755 k3d-linux-amd64 "$bin_dir/k3d"

download "https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl" kubectl
download "https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl.sha256" kubectl.sha256
echo "$(cat kubectl.sha256)  kubectl" | sha256sum --check
install -m 0755 kubectl "$bin_dir/kubectl"

trivy_archive="trivy_0.74.0_Linux-64bit.tar.gz"
download "https://github.com/aquasecurity/trivy/releases/download/v0.74.0/$trivy_archive" "$trivy_archive"
download "https://github.com/aquasecurity/trivy/releases/download/v0.74.0/trivy_0.74.0_checksums.txt" trivy-checksums.txt
trivy_checksum="$(awk -v archive="$trivy_archive" '$2 == archive {print $1}' trivy-checksums.txt)"
test -n "$trivy_checksum"
echo "$trivy_checksum  $trivy_archive" | sha256sum --check
tar --extract --gzip --file "$trivy_archive" trivy
install -m 0755 trivy "$bin_dir/trivy"

k6_archive="k6-v2.2.0-linux-amd64.tar.gz"
download "https://github.com/grafana/k6/releases/download/v2.2.0/$k6_archive" "$k6_archive"
download "https://github.com/grafana/k6/releases/download/v2.2.0/k6-v2.2.0-checksums.txt" k6-checksums.txt
k6_checksum="$(awk -v archive="$k6_archive" '$2 == archive {print $1}' k6-checksums.txt)"
test -n "$k6_checksum"
echo "$k6_checksum  $k6_archive" | sha256sum --check
tar --extract --gzip --file "$k6_archive"
install -m 0755 k6-v2.2.0-linux-amd64/k6 "$bin_dir/k6"

echo "$bin_dir" >> "${GITHUB_PATH:?GITHUB_PATH is required}"
