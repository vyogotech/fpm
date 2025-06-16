# Frappe Package Manager (FPM) CLI

FPM is a command-line interface to manage Frappe applications, providing package creation, installation, and repository management to streamline Frappe app deployment.

## Local Development Setup

This project supports development using [Dev Containers (Visual Studio Code Remote - Containers)](https://code.visualstudio.com/docs/remote/containers). This ensures a consistent and easy-to-set-up development environment.

**Prerequisites:**
*   [Docker Desktop](https://www.docker.com/products/docker-desktop/)
*   [Visual Studio Code](https://code.visualstudio.com/)
*   [VS Code Remote - Containers extension](https://marketplace.visualstudio.com/items?itemName=ms-vscode-remote.remote-containers)

**Getting Started:**
1.  Clone this repository:
    ```bash
    git clone <repository-url>
    cd fpm
    ```
2.  Open the `fpm` directory in Visual Studio Code.
3.  When prompted, click on "Reopen in Container". VS Code will build the dev container and set up the environment. (If not prompted, open the Command Palette (Ctrl+Shift+P or Cmd+Shift+P) and select "Remote-Containers: Reopen in Container".)

Once the container is built and your VS Code window has reloaded, you'll be in a development environment with Go 1.22 and the necessary Go tools installed.

## Building

To build the FPM CLI from source within the dev container (or any Go 1.22 environment):

```bash
go build -o ./bin/fpm ./cmd/fpm/main.go
```
This will create an executable at `./bin/fpm`.

## Usage

The FPM CLI provides several commands to manage Frappe packages:

```
fpm --help
```

Available commands (this list will grow):
*   `fpm package`: Package a Frappe application into an `.fpm` file.
    *   `--source <path>`: Path to the Frappe app source directory (default: current directory).
    *   `--output-path <path>`: Directory where the `.fpm` file will be saved (default: current directory).
    *   `--version <version>`: The version for the package (e.g., `1.0.0`). This flag is required.
    *   `--overwrite`: Allows overwriting an existing `.fpm` file if it has the same name and version.

    The command reads package details from an `app_metadata.json` file in the source directory. If this file doesn't exist, a basic one will be generated. You can use a `.fpmignore` file (similar to `.gitignore`) in your app's source directory to specify files and directories to exclude from the package.
*   `fpm install`: Install a Frappe application package.
*   `fpm publish`: Publish a Frappe application package to a repository.
*   `fpm repo add`: Add a new Frappe package repository.
*   `fpm deps`: Inspect package dependencies.

For more detailed help on a specific command:
```
fpm [command] --help
```

## Contributing
(Details to be added)
