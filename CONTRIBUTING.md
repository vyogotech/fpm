# Contributing to FPM (Frappe Package Manager)

Thank you for your interest in contributing to FPM! This guide will help you get started with development.

## 📋 Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Setup](#development-setup)
- [Making Changes](#making-changes)
- [Testing](#testing)
- [Pull Request Process](#pull-request-process)
- [Coding Standards](#coding-standards)
- [Project Structure](#project-structure)

## 📜 Code of Conduct

By participating in this project, you agree to:

- Be respectful and inclusive
- Welcome newcomers and help them learn
- Focus on what is best for the community
- Show empathy towards other community members

## 🚀 Getting Started

### Prerequisites

- **Go 1.22.2 or higher** - [Install Go](https://golang.org/doc/install)
- **Git** - [Install Git](https://git-scm.com/downloads)
- **Docker/Podman** (optional) - For testing repository deployments
- **VS Code** (recommended) - [Download VS Code](https://code.visualstudio.com/)

### Fork and Clone

1. Fork the repository on GitHub
2. Clone your fork:

```bash
git clone https://github.com/YOUR-USERNAME/fpm.git
cd fpm
```

3. Add upstream remote:

```bash
git remote add upstream https://github.com/ORIGINAL-OWNER/fpm.git
```

4. Create a feature branch:

```bash
git checkout -b feature/my-new-feature
```

## 🛠️ Development Setup

### Option 1: Dev Containers (Recommended)

The easiest way to get started is using VS Code Dev Containers:

1. Install:
   - [Docker Desktop](https://www.docker.com/products/docker-desktop/)
   - [VS Code](https://code.visualstudio.com/)
   - [Remote - Containers extension](https://marketplace.visualstudio.com/items?itemName=ms-vscode-remote.remote-containers)

2. Open project in VS Code:
   ```bash
   code fpm/
   ```

3. When prompted, click "Reopen in Container"
   - Or: `Ctrl/Cmd + Shift + P` → "Remote-Containers: Reopen in Container"

4. The container will build with Go 1.22.2 and all tools pre-installed

### Option 2: Local Setup

If you prefer local development:

```bash
# Install dependencies
go mod download

# Verify installation
go version  # Should show go1.22.2 or higher

# Build the project
go build -o fpm ./cmd/fpm/main.go

# Run tests
go test ./...

# Run the binary
./fpm --help
```

## 🔨 Making Changes

### Development Workflow

1. **Sync with upstream**:
   ```bash
   git fetch upstream
   git rebase upstream/main
   ```

2. **Make your changes**:
   - Write clean, readable code
   - Follow Go conventions
   - Add tests for new features
   - Update documentation

3. **Test your changes**:
   ```bash
   # Run all tests
   go test -v ./...
   
   # Run tests with race detection
   go test -race ./...
   
   # Run tests with coverage
   go test -coverprofile=coverage.out ./...
   go tool cover -html=coverage.out
   ```

4. **Format code**:
   ```bash
   go fmt ./...
   gofmt -s -w .
   ```

5. **Check for issues**:
   ```bash
   go vet ./...
   ```

### What to Work On

- Check [Issues](https://github.com/vyogotech/fpm/issues) for `good first issue` or `help wanted` labels
- Look at the [Roadmap](CHANGELOG.md#unreleased) for planned features
- Fix bugs or improve documentation
- Propose new features (open an issue first to discuss)

### Areas Where We Need Help

#### Priority 1: Authentication Support
Add username/password support to repository configuration:
- Modify `internal/config/config.go` to add auth fields
- Update `internal/repository/remote.go` to use credentials
- Add secure credential storage (consider keychain/credential manager)

#### Priority 2: Automatic Directory Creation
Fix nginx limitation for nested directory creation:
- Implement pre-flight directory creation in FPM before upload
- Or: Create a lightweight upload proxy

#### Priority 3: TLS/SSL Support
- Add certificate management to repository deployment
- Support Let's Encrypt auto-renewal
- Document manual certificate configuration

#### Priority 4: Web UI
Create a web interface for repository browsing:
- Package search and browsing
- Version history
- Download statistics
- Admin panel

#### Priority 5: Testing
- Increase test coverage (currently ~60%)
- Add integration tests
- Add end-to-end tests

## 🧪 Testing

### Running Tests

```bash
# All tests
go test ./...

# Specific package
go test ./internal/repository

# Verbose output
go test -v ./...

# With coverage
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

### Writing Tests

- Place tests in `*_test.go` files
- Use table-driven tests when appropriate
- Mock external dependencies
- Aim for >80% coverage on new code

Example:

```go
func TestPackageCreation(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"valid package", "test-app", "test-app-1.0.0.fpm", false},
        {"invalid name", "", "", true},
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := CreatePackage(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("CreatePackage() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if got != tt.want {
                t.Errorf("CreatePackage() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Testing Repository Deployment

```bash
# Start test repository
cd /path/to/fpm
./scripts/setup.sh
podman compose up -d

# Test packaging
mkdir -p /tmp/test-app/test_app
echo '__version__ = "1.0.0"' > /tmp/test-app/test_app/__init__.py
cd /tmp/test-app
/path/to/fpm package --version 1.0.0 --org testorg --app-name test_app

# Test publishing
/path/to/fpm repo add local-test http://localhost:8080 --priority 1
/path/to/fpm publish testorg/test_app==1.0.0

# Clean up
podman compose down
```

## 📝 Pull Request Process

### Before Submitting

1. ✅ Tests pass: `go test ./...`
2. ✅ Code is formatted: `go fmt ./...`
3. ✅ No linter errors: `go vet ./...`
4. ✅ Documentation updated
5. ✅ CHANGELOG.md updated (for notable changes)

### Submitting

1. **Push to your fork**:
   ```bash
   git push origin feature/my-new-feature
   ```

2. **Create Pull Request** on GitHub:
   - Use a descriptive title
   - Reference related issues (e.g., "Fixes #123")
   - Describe what changed and why
   - Add screenshots for UI changes
   - List breaking changes (if any)

3. **PR Template**:

   ```markdown
   ## Description
   Brief description of what this PR does.
   
   ## Related Issues
   Fixes #123
   Related to #456
   
   ## Changes Made
   - Added feature X
   - Fixed bug Y
   - Updated documentation for Z
   
   ## Testing
   - [ ] Unit tests added/updated
   - [ ] Integration tests pass
   - [ ] Manually tested with example app
   
   ## Screenshots (if applicable)
   
   ## Breaking Changes
   None / List any breaking changes
   
   ## Checklist
   - [ ] Code follows project style guidelines
   - [ ] Tests pass locally
   - [ ] Documentation updated
   - [ ] CHANGELOG.md updated
   ```

### Review Process

- Maintainers will review within 1-2 business days
- Address feedback promptly
- Keep PR focused (one feature/fix per PR)
- Rebase if requested to resolve conflicts

### After Merge

- Delete your feature branch
- Pull latest changes:
  ```bash
  git checkout main
  git pull upstream main
  ```

## 📐 Coding Standards

### Go Style Guide

Follow the [Effective Go](https://golang.org/doc/effective_go.html) guidelines and:

#### Naming Conventions
- **Packages**: lowercase, single word (e.g., `repository`, `config`)
- **Files**: lowercase with underscores (e.g., `package_manager.go`)
- **Types**: PascalCase (e.g., `PackageMetadata`)
- **Functions**: PascalCase for exported, camelCase for unexported
- **Variables**: camelCase (e.g., `packageName`)
- **Constants**: PascalCase or ALL_CAPS for exported

#### Code Organization
- Keep functions short and focused
- Use meaningful variable names
- Add comments for exported functions and types
- Group related code together
- Avoid deep nesting (max 3-4 levels)

#### Error Handling
```go
// ✅ Good: Wrap errors with context
if err != nil {
    return fmt.Errorf("failed to create package: %w", err)
}

// ❌ Bad: Generic error messages
if err != nil {
    return err
}
```

#### Comments
```go
// ✅ Good: Clear, explains why
// PackageMetadata describes the structure for package metadata.
// It includes version information and dependencies used during
// installation and dependency resolution.
type PackageMetadata struct {
    // ...
}

// ❌ Bad: Obvious or outdated
// This is a struct
type PackageMetadata struct {
    // ...
}
```

### File Structure

```
fpm/
├── cmd/                    # Command-line entry points
│   └── fpm/
│       └── main.go        # Main entry point
├── internal/              # Private application code
│   ├── app/              # Application domain logic
│   ├── config/           # Configuration management
│   ├── repository/       # Repository operations
│   └── utils/            # Utility functions
├── pkg/                  # Public libraries (optional)
├── docs/                 # Additional documentation
├── scripts/              # Build and setup scripts
└── tests/                # Integration tests
```

## 🏗️ Project Structure

### Key Packages

- **`cmd/`**: CLI commands using Cobra framework
  - Each command in its own file
  - `root.go` defines the base command

- **`internal/app/`**: Core application logic
  - Package creation and validation
  - App metadata handling

- **`internal/config/`**: Configuration management
  - FPM configuration loading/saving
  - Repository configuration

- **`internal/repository/`**: Repository operations
  - Remote package fetching
  - Package publishing
  - Metadata management

- **`internal/metadata/`**: Package metadata
  - Reading from `.fpm` archives
  - Metadata validation

- **`internal/utils/`**: Utility functions
  - Checksum calculation
  - File operations

### Adding a New Command

1. Create `cmd/mycommand.go`:

```go
package cmd

import (
    "github.com/spf13/cobra"
)

var myCommand = &cobra.Command{
    Use:   "mycommand",
    Short: "Brief description",
    Long:  `Longer description...`,
    RunE: func(cmd *cobra.Command, args []string) error {
        // Implementation
        return nil
    },
}

func init() {
    rootCmd.AddCommand(myCommand)
    myCommand.Flags().String("flag", "", "Flag description")
}
```

2. Add tests in `cmd/mycommand_test.go`
3. Update documentation in README.md

## 🐛 Reporting Bugs

### Before Reporting

- Check existing issues
- Verify you're using the latest version
- Try to reproduce with minimal example

### Bug Report Template

```markdown
**Describe the bug**
Clear description of the issue.

**To Reproduce**
Steps to reproduce:
1. Run command '...'
2. See error

**Expected behavior**
What should have happened.

**Actual behavior**
What actually happened.

**Environment:**
- OS: [e.g., Ubuntu 22.04]
- FPM Version: [e.g., 1.0.0]
- Go Version: [e.g., 1.22.2]

**Additional context**
Logs, screenshots, etc.
```

## 💡 Suggesting Features

1. Check if feature already requested
2. Open an issue with:
   - Clear use case
   - Expected behavior
   - Alternatives considered
3. Be open to discussion
4. Consider implementing it yourself!

## 📧 Contact

- **Issues**: [GitHub Issues](https://github.com/vyogotech/fpm/issues)
- **Discussions**: [GitHub Discussions](https://github.com/vyogotech/fpm/discussions)
- **Email**: maintainer@example.com

## 🙏 Recognition

Contributors will be:
- Listed in CONTRIBUTORS.md
- Mentioned in release notes
- Credited in relevant documentation

Thank you for contributing to FPM! 🎉

