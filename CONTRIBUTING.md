# Contributing to mAPI-ng

First off, thank you for considering contributing to mAPI-ng! This project is open source, and we welcome any contribution, from simple bug reports to feature requests and pull requests.

This document provides a high-level overview of how you can contribute.

## Code of Conduct

This project and everyone participating in it is governed by the [mAPI-ng Code of Conduct](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code.

## How Can I Contribute?

### Reporting Bugs

If you find a bug, please open an issue on GitHub. Make sure to include:
- A clear and descriptive title.
- A detailed description of the problem, including steps to reproduce it.
- The version of mAPI-ng you are using.
- Any relevant logs or error messages.

### Suggesting Enhancements

If you have an idea for a new feature or an improvement to an existing one, please open an issue on GitHub. Describe your idea in detail, including the problem it solves and why you think it would be a valuable addition.

### Pull Requests

We welcome pull requests! If you'd like to contribute code, please follow these steps:

1.  **Fork the repository** on GitHub.
2.  **Create a new branch** for your changes (e.g., `feat/my-new-feature` or `fix/a-nasty-bug`).
3.  **Make your changes** in the new branch.
4.  **Test your changes.** Run the local test suite to ensure everything is working correctly:
    ```bash
    make test
    ```
5.  **Lint your code.** Ensure your changes adhere to the project's style and quality standards:
    ```bash
    make audit
    ```
6.  **Tidy your modules.** Make sure all `go.mod` files are up-to-date:
    ```bash
    make tidy
    ```
7.  **Commit your changes** following the [Conventional Commits](https://www.conventionalcommits.org/) format (e.g. `feat: add rollup tier selection`, `fix: bound the cardinality map`). Commit messages are linted, so non-conforming messages will be rejected.
8.  **Push your branch** to your fork on GitHub.
9.  **Open a pull request** to the main mAPI-ng repository.

A project maintainer will review your PR, provide feedback, and merge it if it meets the project's standards.

### Architectural Changes

Substantial or architecture-affecting changes should be accompanied by an Architecture Decision Record under [`docs/adr/`](docs/adr/), numbered sequentially and following the format of the existing records. This keeps the reasoning behind the design discoverable.

## Licensing of Contributions

This repository is licensed under the **MIT License** (see the License section of the [README.md](README.md)). By submitting a contribution, you agree to license it under the MIT License.

If you are not comfortable contributing under the MIT License, please open an issue to discuss before submitting a pull request.

## Development Environment

This project is a Go workspace. For details on the development setup and workflow, please see the **Development** section in the main [README.md](README.md).
