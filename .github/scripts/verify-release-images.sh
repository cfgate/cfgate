#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_VERSION:?release version required}"
: "${RELEASE_SHA:?release commit required}"
mkdir -p release-images
skopeo inspect --raw oci-archive:cfgate.oci.tar > release-images/index.json
index_digest=$(skopeo manifest-digest release-images/index.json)
# Both runnable platforms and their BuildKit attestations must be retained.
jq -e '[.manifests[] | select(.platform.os == "linux") | .platform.architecture] | sort == ["amd64", "arm64"]' release-images/index.json
jq -e '[.manifests[] | select(.annotations["vnd.docker.reference.type"] == "attestation-manifest")] | length >= 2' release-images/index.json
for arch in amd64 arm64; do
  expected=$(jq -er --arg arch "$arch" '.manifests[] | select(.platform.os == "linux" and .platform.architecture == $arch) | .digest' release-images/index.json)
  attestation=$(jq -er --arg digest "$expected" '.manifests[] | select(.annotations["vnd.docker.reference.digest"] == $digest and .annotations["vnd.docker.reference.type"] == "attestation-manifest") | .digest' release-images/index.json)
  [[ "$attestation" =~ ^sha256:[a-f0-9]{64}$ ]]
  tar -xOf cfgate.oci.tar "blobs/sha256/${attestation#sha256:}" > "release-images/$arch-attestation.json"
  jq -e '[.layers[].annotations["in-toto.io/predicate-type"]] | any(startswith("https://slsa.dev/provenance/")) and any(. == "https://spdx.dev/Document")' "release-images/$arch-attestation.json"
  archive="release-images/cfgate-$arch.oci.tar"
  skopeo --override-os linux --override-arch "$arch" copy --preserve-digests oci-archive:cfgate.oci.tar "oci-archive:$archive"
  skopeo inspect --raw "oci-archive:$archive" > "release-images/$arch-manifest.json"
  test "$(skopeo manifest-digest "release-images/$arch-manifest.json")" = "$expected"
  # Trivy reads an OCI layout directory, not an OCI-format tar archive.
  mkdir -p "release-images/$arch"
  tar -xf "$archive" -C "release-images/$arch"
  skopeo inspect "oci-archive:$archive" | jq -e --arg arch "$arch" --arg sha "$RELEASE_SHA" '.Architecture == $arch and .Os == "linux" and .Labels["org.opencontainers.image.revision"] == $sha'
  ref="cfgate:release-smoke-$arch"
  skopeo copy "oci-archive:$archive" "docker-daemon:$ref"
  docker run --rm --platform "linux/$arch" "$ref" --help
  container=$(docker create --platform "linux/$arch" "$ref")
  trap 'docker rm "$container" >/dev/null' EXIT
  docker cp "$container:/manager" "release-images/$arch-manager"
  docker rm "$container" >/dev/null
  trap - EXIT
  go version -m "release-images/$arch-manager" > "release-images/$arch-version.txt"
  grep -F -- "main.Version=$RELEASE_VERSION " "release-images/$arch-version.txt"
  grep -E -- "main.Commit=$RELEASE_SHA([[:space:]]|\")" "release-images/$arch-version.txt"
  grep -Fx -- $'\tbuild\tGOOS=linux' "release-images/$arch-version.txt"
  grep -Fx -- $'\tbuild\tGOARCH='"$arch" "release-images/$arch-version.txt"
done
sha256sum cfgate.oci.tar > cfgate.oci.tar.sha256
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "digest=$index_digest" >> "$GITHUB_OUTPUT"
fi
