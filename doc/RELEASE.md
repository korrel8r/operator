# Releasing a new version

Steps to release a new version X.Y.Z, for maintainers.

On branch `main`:

1. Edit Makefile and set 'VERSION=X.Y.Z'
2. `make pre-release IMG_ORG=quay.io/korrel8r`
3. Verify all changes are version related, commit with message 'Release X.Y.Z' \
4  **NOTE:** Normally the only changes in a release commit are `Makefile` and `version.txt`
5. `make release IMG_ORG=quay.io/korrel8r`
  - Re-runs 'make pre-release', verifies the working tree is clean.
  - Creates and pushes the git tag 'vX.Y.Z'
  - Pushes ':latest' tag for images.
