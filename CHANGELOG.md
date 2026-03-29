# Changelog

All notable changes to this project will be documented in this file.

The format is based on Keep a Changelog and this project follows Semantic Versioning.

## [Unreleased]

### Added
- Initial open-source project scaffolding and CI.

### Changed
- Normalized forced adaptive mode `two` to canonical `two-pass`.
- Improved run failure/status persistence handling in pipeline execution.

### Fixed
- Avoided silent event drops by honoring context cancellation when emitting events.
