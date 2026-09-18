# Contributing

CloudForge welcomes focused changes that improve its ability to provide
measurable production-behavior evidence.

## Development

Use Go 1.27. Run `make check` before opening a pull request. New analyzer
behavior should include a minimal fixture or focused unit test, and runtime
behavior should include cleanup and failure-path coverage.

Work on a feature branch and open a pull request. Feature work is merged only
after CI passes and review is complete. Keep documentation and the changelog
accurate; do not describe planned behavior as implemented.

Please open a focused issue before starting a large architectural change.
